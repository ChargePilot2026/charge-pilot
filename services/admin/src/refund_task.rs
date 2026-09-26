//! Durable refund orchestration. Provider submission always reuses the saved refund number.
use crate::AppState;
use common_error::{AppError, AppResult};
use sqlx::Row;

pub async fn enqueue(st: &AppState, entry: &common_redis::StreamEntry) -> AppResult<()> {
    let no = entry
        .envelope
        .payload
        .get("refund_no")
        .and_then(|v| v.as_str())
        .ok_or_else(|| AppError::BadRequest("退款事件缺少退款单号".into()))?;
    if no.is_empty()
        || no.len() > 64
        || !no
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_-|*@".contains(&b))
    {
        return Err(AppError::BadRequest("退款单号无效".into()));
    }
    sqlx::query("INSERT IGNORE INTO refund_task (refund_no,event_id) VALUES (?,?)")
        .bind(no)
        .bind(&entry.envelope.event_id)
        .execute(st.db.pool())
        .await?;
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
    let mut tx = st.db.pool().begin().await?;
    let row=sqlx::query("SELECT CAST(refund_no AS CHAR CHARACTER SET utf8mb4) AS refund_no,stage,request_json,result_json FROM refund_task WHERE stage IN ('queued','querying','reporting') AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY scheduled_at,refund_no LIMIT 1 FOR UPDATE SKIP LOCKED").fetch_optional(&mut *tx).await?;
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
        sqlx::query("UPDATE refund_task SET attempts=attempts+1,last_error=?,scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 30 SECOND) WHERE refund_no=?").bind(message).bind(&no).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    Ok(true)
}
async fn advance(
    st: &AppState,
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
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
                &api_contracts::paths::USER_INTERNAL_REFUND_RESULT.replace(":refund_id", no),
                &payload,
            )
            .await?;
        if ack.get("ok").and_then(|v| v.as_bool()) != Some(true) {
            return Err(AppError::Conflict("退款结果未被确认".into()));
        }
        sqlx::query("UPDATE refund_task SET stage=?,last_error=NULL WHERE refund_no=?")
            .bind(if payload["success"] == true {
                "done"
            } else {
                "manual_review"
            })
            .bind(no)
            .execute(&mut **tx)
            .await?;
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
            sqlx::query("UPDATE refund_task SET stage=?,last_error=NULL WHERE refund_no=?")
                .bind(if detail.status == "success" {
                    "done"
                } else {
                    "manual_review"
                })
                .bind(no)
                .execute(&mut **tx)
                .await?;
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
        sqlx::query("UPDATE refund_task SET stage='querying',request_json=?,last_error=NULL WHERE refund_no=?").bind(serde_json::to_value(req)?).bind(no).execute(&mut **tx).await?;
        // Commit the immutable provider request before any provider side effect.
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
        sqlx::query("UPDATE refund_task SET scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 60 SECOND),attempts=attempts+1,last_error=NULL WHERE refund_no=?").bind(no).execute(&mut **tx).await?;
    } else {
        let result = serde_json::json!({"refund_no":no,"success":response.status=="SUCCESS","wechat_refund_id":response.refund_id,"failure_reason":if response.status=="SUCCESS"{None}else{Some(format!("微信退款状态 {}，需人工处理",response.status))}});
        sqlx::query("UPDATE refund_task SET stage='reporting',result_json=?,last_error=NULL WHERE refund_no=?").bind(result).bind(no).execute(&mut **tx).await?;
    }
    Ok(())
}

#[cfg(test)]
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
            db: common_db::Db::connect(&cfg.mysql).await.unwrap(),
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
            .execute(st.db.pool())
            .await
            .unwrap();
        let outcome=async {
            for expected in ["querying","querying","reporting","reporting","done"] {
                let mut tx=st.db.pool().begin().await?;
                let row=sqlx::query("SELECT stage,request_json,result_json FROM refund_task WHERE refund_no=? FOR UPDATE").bind(&no).fetch_one(&mut *tx).await?;
                let stage:String=row.try_get("stage")?;
                let step=advance(&st,&mut tx,&no,&stage,row.try_get("request_json")?,row.try_get("result_json")?).await;
                if expected=="reporting" && stage=="reporting" {assert!(step.is_err());}else{step?;}
                tx.commit().await?;
                let actual:String=sqlx::query_scalar("SELECT stage FROM refund_task WHERE refund_no=?").bind(&no).fetch_one(st.db.pool()).await?;assert_eq!(actual,expected);
            }
            assert_eq!(mock_state.submissions.load(Ordering::SeqCst),1);assert_eq!(mock_state.reports.load(Ordering::SeqCst),2);
            Ok::<_,AppError>(())
        }.await;
        sqlx::query("DELETE FROM refund_task WHERE refund_no=?")
            .bind(&no)
            .execute(st.db.pool())
            .await
            .unwrap();
        server.abort();
        std::fs::remove_file(private).unwrap();
        std::fs::remove_file(public).unwrap();
        outcome.unwrap();
    }
}
