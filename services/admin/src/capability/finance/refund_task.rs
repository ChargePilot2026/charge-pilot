//! refund_task —— 持久化退款编排(D11)
//!
//! 每次推进只复用**已保存的退款单号**,绝不新生成:微信侧以
//! `out_refund_no` 为幂等键,重新生成会让"同一次退款的查询与提交"变成两笔。
//!
//! 阶段机:`queued`(向 user 取执行详情)→ `querying`(向微信查询 / 补单提交)→
//! `reporting`(向 user 回报结果)→ `done` / `manual_review`。
//! 每一步的状态都先落库再产生外部副作用,进程死在任意一步都能续跑。
//!
//! `disallowed_types` 豁免：`refund_task.request_json` / `result_json` 与上游
//! 回执都是**原样透出**的 JSON 列（方案 §三例外清单第 2 条）。退款请求与结果
//! 的形状由微信 / user 侧决定，本文件既不定义也不解析，类型化只会强行假设。

#![allow(clippy::disallowed_types)]

use crate::AppState;
use common_error::{AppError, AppResult};
use sqlx::Row;

// 本域的 SQL 统一住在 `finance/repository_sql.rs`（含 `refund_task` 的全部语句），
// 与 usecase 层分离。
use super::repository_sql;

pub async fn enqueue(st: &AppState, entry: &common_redis::StreamEntry) -> AppResult<()> {
    let no = entry
        .envelope
        .payload
        .get("refund_no")
        .and_then(|v| v.as_str())
        .ok_or_else(|| AppError::BadRequest("退款事件缺少退款单号".into()))?;
    if !crate::capability::finance::domain::valid_refund_no(no) {
        return Err(AppError::BadRequest("退款单号无效".into()));
    }
    repository_sql::enqueue_refund_task(st, no, &entry.envelope.event_id).await?;
    Ok(())
}

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(std::time::Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..25 {
                match drive_one(&st).await {
                    Ok(true) => {}
                    Ok(false) => break,
                    Err(error) => {
                        tracing::warn!(%error,"refund task transaction failed; will retry");
                        break;
                    }
                }
            }
        }
    });
}

async fn drive_one(st: &AppState) -> AppResult<bool> {
    let mut tx = st.finance.begin().await?;
    let row = repository_sql::claim_refund_task(&mut tx).await?;
    let Some(row) = row else {
        tx.rollback().await?;
        return Ok(false);
    };
    let no: String = row.try_get("refund_no")?;
    let stage: String = row.try_get("stage")?;
    let result = advance(
        st,
        &mut tx,
        &no,
        &stage,
        row.try_get("request_json")?,
        row.try_get("result_json")?,
    )
    .await;
    if let Err(error) = result {
        // Config/network failures remain retryable. No failed connection is proof of failed refund.
        let message = error.to_string().chars().take(255).collect::<String>();
        repository_sql::defer_refund_task(&mut tx, &no, &message).await?;
    }
    tx.commit().await?;
    Ok(true)
}

async fn advance(
    st: &AppState,
    tx: &mut common_db::Tx<'_>,
    no: &str,
    stage: &str,
    request: Option<serde_json::Value>,
    result: Option<serde_json::Value>,
) -> AppResult<()> {
    let client = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    if stage == "reporting" {
        let payload = result.ok_or_else(|| AppError::Conflict("退款任务缺少结果".into()))?;
        let ack: serde_json::Value = client
            .post(
                st.cfg.service_urls.user.as_deref(),
                &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_REFUND_RESULT, "refund_id", no),
                &payload,
            )
            .await?;
        if ack.get("ok").and_then(|v| v.as_bool()) != Some(true) {
            return Err(AppError::Conflict("退款结果未被确认".into()));
        }
        let next = crate::capability::finance::domain::stage_from_reported_success(
            payload["success"] == true,
        );
        repository_sql::set_refund_stage(tx, no, next).await?;
        return Ok(());
    }
    let cfg = st
        .cfg
        .wechat
        .as_ref()
        .ok_or_else(|| AppError::Config("缺少微信退款配置".into()))?;
    common_wechat::validate_pay_config(cfg)?;
    if stage == "queued" {
        let detail: api_contracts::refunds::ExecutionDetail = client
            .post(
                st.cfg.service_urls.user.as_deref(),
                api_contracts::paths::USER_INTERNAL_REFUND_EXECUTION,
                &api_contracts::refunds::ExecutionRequest {
                    refund_no: no.into(),
                },
            )
            .await?;
        if detail.refund_no != no {
            return Err(AppError::Conflict("退款任务身份不一致".into()));
        }
        if detail.status == "success" || detail.status == "failed" {
            let next = if detail.status == "success" {
                "done"
            } else {
                "manual_review"
            };
            repository_sql::set_refund_stage(tx, no, next).await?;
            return Ok(());
        }
        if detail.status != "processing" {
            return Err(AppError::Conflict("退款尚未允许执行".into()));
        }
        let req = common_wechat::RefundReq {
            transaction_id: Some(detail.transaction_id),
            out_trade_no: None,
            out_refund_no: no.into(),
            reason: Some("充电订单退款".into()),
            notify_url: cfg.refund_notify_url.clone(),
            funds_account: None,
            amount: common_wechat::RefundAmount {
                refund: detail.refund_cents,
                total: detail.total_cents,
                currency: "CNY".into(),
            },
        };
        // Commit the immutable provider request before any provider side effect.
        repository_sql::set_refund_request(tx, no, &serde_json::to_value(req)?).await?;
        return Ok(());
    }
    let req: common_wechat::RefundReq = serde_json::from_value(
        request.ok_or_else(|| AppError::Conflict("退款任务缺少请求快照".into()))?,
    )?;
    let response = match common_wechat::query_refund(&st.http, cfg, &req).await {
        Err(AppError::NotFound(_)) => common_wechat::refund_once(&st.http, cfg, &req).await?,
        other => other?,
    };
    if response.status == "PROCESSING" {
        repository_sql::reschedule_refund_query(tx, no).await?;
    } else {
        let success = response.status=="SUCCESS";
        let result = serde_json::to_value(RefundOutcome {
            refund_no: no,
            success,
            // 原 `json!` 写的是裸字符串；`Option<String>` 包一层序列化后字节相同。
            wechat_refund_id: Some(response.refund_id),
            failure_reason: if success {None} else {Some(format!("微信退款状态 {}，需人工处理",response.status))},
        })?;
        repository_sql::set_refund_result(tx, no, &result).await?;
    }
    Ok(())
}

/// 微信查询终态写回 `refund_task.result_json` 的载荷。
/// 键集合固定且形状已知,故用具名结构体而非 `json!`。
#[derive(serde::Serialize)]
struct RefundOutcome<'a> {
    refund_no: &'a str,
    success: bool,
    wechat_refund_id: Option<String>,
    failure_reason: Option<String>,
}

/// D11 验收:驱动单步推进。provider 与 user 服务均以本地 mock 替代,
/// 真实依赖只有 admin MySQL 与 Redis。
#[cfg(test)]
// 验收测试自带 mock provider / mock user 服务，并直接读写 refund_task 表，
// 因此 SQL 与 `json!` 属于测试自身的职责。
#[allow(clippy::disallowed_methods, clippy::disallowed_macros, clippy::disallowed_types)]
mod tests {
    use super::*;
    use axum::{
        body::Bytes,
        extract::State,
        http::{StatusCode, Uri},
        response::IntoResponse,
    };
    use base64::Engine;
    use rsa::pkcs8::{EncodePrivateKey, EncodePublicKey, LineEnding};
    use sha2::Digest;
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };
    #[derive(Clone)]
    struct Mock {
        key: Arc<rsa::RsaPrivateKey>,
        no: String,
        submissions: Arc<AtomicUsize>,
        reports: Arc<AtomicUsize>,
    }
    async fn mock(
        State(m): State<Mock>,
        method: axum::http::Method,
        uri: Uri,
        body: Bytes,
    ) -> axum::response::Response {
        if uri.path() == api_contracts::paths::USER_INTERNAL_REFUND_EXECUTION {
            let request: serde_json::Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(request["refund_no"], m.no);
            return axum::Json(serde_json::json!({"code":0,"message":"ok","data":{"refund_no":m.no,"status":"processing","transaction_id":"420001","refund_cents":82,"total_cents":100,"wechat_refund_id":null},"request_id":"test"})).into_response();
        }
        if uri.path().ends_with("/result") {
            let request: serde_json::Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(request["success"], true);
            assert_eq!(request["refund_no"], m.no);
            if m.reports.fetch_add(1, Ordering::SeqCst) == 0 {
                return StatusCode::SERVICE_UNAVAILABLE.into_response();
            }
            return axum::Json(
                serde_json::json!({"code":0,"message":"ok","data":{"ok":true},"request_id":"test"}),
            )
            .into_response();
        }
        let (status, raw) = if method == axum::http::Method::GET
            && m.submissions.load(Ordering::SeqCst) == 0
        {
            (
                StatusCode::NOT_FOUND,
                serde_json::json!({"code":"RESOURCE_NOT_EXISTS"}).to_string(),
            )
        } else {
            let status = if method == axum::http::Method::POST {
                let request: serde_json::Value = serde_json::from_slice(&body).unwrap();
                assert_eq!(request["out_refund_no"], m.no);
                assert_eq!(request["amount"]["refund"], 82);
                m.submissions.fetch_add(1, Ordering::SeqCst);
                "PROCESSING"
            } else {
                "SUCCESS"
            };
            (StatusCode::OK,serde_json::json!({"refund_id":"500001","out_refund_no":m.no,"transaction_id":"420001","out_trade_no":"PAY_test","status":status,"amount":{"refund":82,"total":100,"currency":"CNY"}}).to_string())
        };
        let ts = chrono::Utc::now().timestamp().to_string();
        let digest = sha2::Sha256::digest(format!("{ts}\nnonce\n{raw}\n"));
        let sig = base64::engine::general_purpose::STANDARD.encode(
            m.key
                .sign(rsa::Pkcs1v15Sign::new::<sha2::Sha256>(), &digest)
                .unwrap(),
        );
        (
            status,
            [
                ("wechatpay-serial", "PUB_KEY_ID_TEST".to_string()),
                ("wechatpay-timestamp", ts),
                ("wechatpay-nonce", "nonce".to_string()),
                ("wechatpay-signature", sig),
            ],
            raw,
        )
            .into_response()
    }
    #[tokio::test]
    #[ignore = "requires development admin MySQL and Redis; provider/user are local mocks"]
    async fn durable_submission_query_and_result_retry() {
        let mut cfg = common_config::AppConfig::load().unwrap();
        let no = format!("RFTEST_{}", uuid::Uuid::new_v4().simple());
        let key = Arc::new(rsa::RsaPrivateKey::new(&mut rand::rngs::OsRng, 2048).unwrap());
        let private = std::env::temp_dir().join(format!("{no}.key"));
        let public = std::env::temp_dir().join(format!("{no}.pub"));
        std::fs::write(
            &private,
            key.to_pkcs8_pem(LineEnding::LF).unwrap().as_bytes(),
        )
        .unwrap();
        std::fs::write(
            &public,
            key.to_public_key()
                .to_public_key_pem(LineEnding::LF)
                .unwrap(),
        )
        .unwrap();
        let mock_state = Mock {
            key,
            no: no.clone(),
            submissions: Arc::new(AtomicUsize::new(0)),
            reports: Arc::new(AtomicUsize::new(0)),
        };
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let app = axum::Router::new()
            .fallback(mock)
            .with_state(mock_state.clone());
        let server = tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        cfg.service_urls.user = Some(base.clone());
        let wechat = cfg.wechat.as_mut().unwrap();
        wechat.mch_id = "1900000109".into();
        wechat.merchant_serial_no = Some("AB12".into());
        wechat.platform_key_id = Some("PUB_KEY_ID_TEST".into());
        wechat.private_key_path = Some(private.to_string_lossy().into());
        wechat.platform_public_key_path = Some(public.to_string_lossy().into());
        wechat.refund_url = format!("{base}/v3/refund/domestic/refunds");
        let st = AppState {
            services: crate::services::build(
                common_db::Db::connect(&cfg.mysql).await.unwrap(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                Arc::new(cfg.clone()),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.unwrap(),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.unwrap(),
            ),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache)
                .await
                .unwrap(),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream)
                .await
                .unwrap(),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: Arc::new(cfg),
        };
        sqlx::query("INSERT INTO refund_task (refund_no,event_id) VALUES (?,?)")
            .bind(&no)
            .bind(uuid::Uuid::new_v4().to_string())
            .execute(st.finance.pool())
            .await
            .unwrap();
        let outcome=async {
            for expected in ["querying","querying","reporting","reporting","done"] {
                let mut tx=st.finance.begin().await?;
                let row=sqlx::query("SELECT stage,request_json,result_json FROM refund_task WHERE refund_no=? FOR UPDATE").bind(&no).fetch_one(tx.executor()).await?;
                let stage:String=row.try_get("stage")?;
                let step=advance(&st,&mut tx,&no,&stage,row.try_get("request_json")?,row.try_get("result_json")?).await;
                if expected=="reporting" && stage=="reporting" {assert!(step.is_err());}else{step?;}
                tx.commit().await?;
                let actual:String=sqlx::query_scalar("SELECT stage FROM refund_task WHERE refund_no=?").bind(&no).fetch_one(st.finance.pool()).await?;assert_eq!(actual,expected);
            }
            assert_eq!(mock_state.submissions.load(Ordering::SeqCst),1);assert_eq!(mock_state.reports.load(Ordering::SeqCst),2);
            Ok::<_,AppError>(())
        }.await;
        sqlx::query("DELETE FROM refund_task WHERE refund_no=?")
            .bind(&no)
            .execute(st.finance.pool())
            .await
            .unwrap();
        server.abort();
        std::fs::remove_file(private).unwrap();
        std::fs::remove_file(public).unwrap();
        outcome.unwrap();
    }
}
