//! Recover the same checkout, preserving its original WeChat payment number and expiry.
use crate::{api_types::ScanStartResponse, AppState};
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;
fn conflict() -> AppError {
    AppError::Conflict("订单已支付、已失效或端口预留已结束，请刷新订单状态".into())
}
pub async fn resume(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Path(order): Path<String>,
) -> AppResult<Json<ApiEnvelope<ScanStartResponse>>> {
    Ok(Json(ApiEnvelope::ok(
        prepare(&st, claims.user_id, &claims.sub, &order).await?,
        common_error::current_request_id(),
    )))
}
pub async fn prepare(
    st: &AppState,
    uid: u64,
    openid: &str,
    order: &str,
) -> AppResult<ScanStartResponse> {
    let ids:Vec<(u64,Option<u64>)>=sqlx::query_as("SELECT id,payment_order_id FROM charge_order WHERE order_no=? AND user_id=? AND deleted_at IS NULL").bind(order).bind(uid).fetch_all(st.db.pool()).await?;
    if ids.len() != 1 {
        return Err(AppError::NotFound("订单不存在".into()));
    }
    let (cid, pid) = ids[0];
    let pid = pid.ok_or_else(conflict)?;
    let mut tx = st.db.pool().begin().await?;
    let pays=sqlx::query("SELECT order_no,biz_id,total_cents,status,expired_at FROM payment_order WHERE id=? AND user_id=? AND biz_type='charge' AND pay_method='wechat' AND deleted_at IS NULL FOR UPDATE").bind(pid).bind(uid).fetch_all(&mut *tx).await?;
    if pays.len() != 1 {
        return Err(conflict());
    }
    let pay = &pays[0];
    let rows=sqlx::query("SELECT payment_order_id,status,port_code FROM charge_order WHERE id=? AND user_id=? AND deleted_at IS NULL FOR UPDATE").bind(cid).bind(uid).fetch_all(&mut *tx).await?;
    if rows.len() != 1 {
        return Err(conflict());
    }
    let charge = &rows[0];
    if pay.try_get::<u64, _>("biz_id")? != cid
        || charge.try_get::<Option<u64>, _>("payment_order_id")? != Some(pid)
        || pay.try_get::<String, _>("status")? != "initiated"
        || charge.try_get::<String, _>("status")? != "pending_payment"
    {
        return Err(conflict());
    }
    let expiry = pay
        .try_get::<Option<chrono::NaiveDateTime>, _>("expired_at")?
        .ok_or_else(conflict)?
        .and_utc();
    let port = charge
        .try_get::<Option<String>, _>("port_code")?
        .ok_or_else(conflict)?;
    let hold = common_redis::PortLock::new(st.redis_cache.clone());
    if expiry <= chrono::Utc::now() || !hold.check_holder(&port, &format!("{uid}:{order}")).await? {
        return Err(conflict());
    }
    let saved: Option<(serde_json::Value, Option<String>)> = sqlx::query_as(
        "SELECT request_json,prepay_id FROM charge_prepay WHERE charge_order_id=? FOR UPDATE",
    )
    .bind(cid)
    .fetch_optional(&mut *tx)
    .await?;
    let (raw, existing) =
        saved.ok_or_else(|| AppError::Conflict("该订单缺少预支付记录，请联系客服".into()))?;
    let req: common_wechat::JsapiOrderReq = serde_json::from_value(raw)?;
    let cfg = st
        .cfg
        .wechat
        .as_ref()
        .ok_or_else(|| AppError::Config("缺少微信配置".into()))?;
    let payment_no: String = pay.try_get("order_no")?;
    let request_expiry =
        chrono::DateTime::parse_from_rfc3339(&req.time_expire).map_err(|_| conflict())?;
    if req.out_trade_no != payment_no
        || req.payer.openid != openid
        || i64::from(req.amount.total) != pay.try_get::<i64, _>("total_cents")?
        || req.appid != cfg.appid
        || req.mchid != cfg.mch_id
        || req.amount.currency != "CNY"
        || request_expiry.timestamp_millis() != expiry.timestamp_millis()
    {
        return Err(conflict());
    }
    common_wechat::validate_pay_config(cfg)?;
    let prepay_id = match existing {
        Some(id) => id,
        None => {
            let response = common_wechat::jsapi_create_order(&st.http, cfg, &req).await?;
            sqlx::query("UPDATE charge_prepay SET prepay_id=? WHERE charge_order_id=?")
                .bind(&response.prepay_id)
                .bind(cid)
                .execute(&mut *tx)
                .await?;
            response.prepay_id
        }
    };
    let params = common_wechat::sign_jsapi_pay(cfg, &prepay_id)?;
    tx.commit().await?;
    if expiry <= chrono::Utc::now() || !hold.check_holder(&port, &format!("{uid}:{order}")).await? {
        return Err(conflict());
    }
    Ok(ScanStartResponse {
        amount_cents: i64::from(req.amount.total),
        order_no: order.into(),
        payment_order_no: payment_no,
        hold_expires_at: expiry.to_rfc3339(),
        payment_params: serde_json::to_value(params)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use rsa::pkcs8::{EncodePrivateKey, EncodePublicKey, LineEnding};
    use std::sync::Arc;
    #[tokio::test]
    #[ignore = "requires development user MySQL and Redis"]
    async fn resumes_saved_payment_and_rejects_wrong_owner_paid_expired_or_lost_hold() {
        let mut cfg = common_config::AppConfig::load().unwrap();
        let tag = format!("PP_{}", uuid::Uuid::new_v4().simple());
        let key = rsa::RsaPrivateKey::new(&mut rand::rngs::OsRng, 2048).unwrap();
        let private = std::env::temp_dir().join(format!("{tag}.key"));
        let public = std::env::temp_dir().join(format!("{tag}.pub"));
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
        let wechat = cfg.wechat.as_mut().unwrap();
        wechat.mch_id = "1900000109".into();
        wechat.merchant_serial_no = Some("AB12".into());
        wechat.platform_key_id = Some("PUB_KEY_ID_TEST".into());
        wechat.private_key_path = Some(private.to_string_lossy().into());
        wechat.platform_public_key_path = Some(public.to_string_lossy().into());
        wechat.pay_base_url = "http://127.0.0.1:1".into();
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
        let expiry =
            chrono::DateTime::from_timestamp_millis(chrono::Utc::now().timestamp_millis() + 300000)
                .unwrap();
        let port = api_contracts::ScanPortDetail {
            port_id: tag.clone(),
            port_code: tag.clone(),
            device_id: tag.clone(),
            port_no: 1,
            status: "idle".into(),
        };
        let mut tx = st.db.pool().begin().await.unwrap();
        let cid = crate::checkout::persist_pending(
            &mut tx,
            123,
            &tag,
            &format!("PAY_{tag}"),
            &port,
            &api_contracts::QuoteResponse {
                electric_cents: 80,
                service_cents: 20,
                total_cents: 100,
            },
            expiry,
        )
        .await
        .unwrap();
        let wechat = st.cfg.wechat.as_ref().unwrap();
        let request = common_wechat::JsapiOrderReq {
            appid: wechat.appid.clone(),
            mchid: wechat.mch_id.clone(),
            out_trade_no: format!("PAY_{tag}"),
            description: "test".into(),
            time_expire: expiry.to_rfc3339(),
            attach: None,
            notify_url: wechat.notify_url.clone(),
            amount: common_wechat::JsapiAmount {
                total: 100,
                currency: "CNY".into(),
            },
            payer: common_wechat::JsapiPayer {
                openid: tag.clone(),
            },
        };
        sqlx::query("INSERT INTO charge_prepay (charge_order_id,request_json,prepay_id) VALUES (?,?,'cached_test')").bind(cid).bind(serde_json::to_value(request).unwrap()).execute(&mut *tx).await.unwrap();
        tx.commit().await.unwrap();
        let hold = common_redis::PortLock::new(st.redis_cache.clone());
        hold.try_hold(&tag, &format!("123:{tag}"), 300)
            .await
            .unwrap();
        let first = prepare(&st, 123, &tag, &tag).await;
        let second = prepare(&st, 123, &tag, &tag).await;
        let wrong_owner = prepare(&st, 124, &tag, &tag).await.is_err();
        let wrong_openid = prepare(&st, 123, "other", &tag).await.is_err();
        sqlx::query("UPDATE charge_order SET status='paid' WHERE id=?")
            .bind(cid)
            .execute(st.db.pool())
            .await
            .unwrap();
        let paid = prepare(&st, 123, &tag, &tag).await.is_err();
        sqlx::query("UPDATE charge_order SET status='pending_payment' WHERE id=?")
            .bind(cid)
            .execute(st.db.pool())
            .await
            .unwrap();
        sqlx::query("UPDATE payment_order SET expired_at=DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 SECOND) WHERE biz_type='charge' AND biz_id=?").bind(cid).execute(st.db.pool()).await.unwrap();
        let expired = prepare(&st, 123, &tag, &tag).await.is_err();
        sqlx::query("UPDATE payment_order SET expired_at=? WHERE biz_type='charge' AND biz_id=?")
            .bind(expiry.naive_utc())
            .bind(cid)
            .execute(st.db.pool())
            .await
            .unwrap();
        hold.release_if_match(&tag, &format!("123:{tag}"))
            .await
            .unwrap();
        let lost = prepare(&st, 123, &tag, &tag).await.is_err();
        let mut tx = st.db.pool().begin().await.unwrap();
        sqlx::query("DELETE FROM charge_prepay WHERE charge_order_id=?")
            .bind(cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("DELETE FROM charge_event_log WHERE charge_order_id=?")
            .bind(cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("DELETE FROM payment_order WHERE biz_type='charge' AND biz_id=?")
            .bind(cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("DELETE FROM charge_order WHERE id=?")
            .bind(cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        tx.commit().await.unwrap();
        std::fs::remove_file(private).unwrap();
        std::fs::remove_file(public).unwrap();
        let first = first.unwrap();
        let second = second.unwrap();
        assert_eq!(first.payment_order_no, second.payment_order_no);
        assert_eq!(first.payment_params["package"], "prepay_id=cached_test");
        assert_eq!(first.amount_cents, 100);
        assert_eq!(first.hold_expires_at, expiry.to_rfc3339());
        assert!(wrong_owner && wrong_openid && paid && expired && lost);
    }
}
