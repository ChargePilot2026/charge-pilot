//! Persist the verified port and payment quote together before contacting payment.
use api_contracts::{QuoteResponse, ScanPortDetail};
use chrono::{DateTime, Utc};
use common_error::{AppError, AppResult};
use sqlx::{MySql, Transaction};

pub fn validate_quote(quote: &QuoteResponse) -> AppResult<i32> {
    if quote.electric_cents < 0 || quote.service_cents < 0 || quote.total_cents <= 0
        || quote.electric_cents.checked_add(quote.service_cents) != Some(quote.total_cents)
    {
        return Err(AppError::ServiceUnavailable("计费服务返回了无效金额".into()));
    }
    i32::try_from(quote.total_cents)
        .map_err(|_| AppError::ServiceUnavailable("支付金额超出支持范围".into()))
}

pub async fn persist_pending(
    tx: &mut Transaction<'_, MySql>, user_id: u64, order_no: &str, payment_no: &str,
    port: &ScanPortDetail, quote: &QuoteResponse, expires_at: DateTime<Utc>,
) -> AppResult<u64> {
    validate_quote(quote)?;
    let month = Utc::now().format("%Y-%m-01").to_string();
    let order_id = sqlx::query(
        "INSERT INTO charge_order (order_no,user_id,device_id,port_no,port_code,status,created_month) \
         VALUES (?,?,?,?,?,'pending_payment',?)"
    ).bind(order_no).bind(user_id).bind(&port.device_id).bind(port.port_no)
        .bind(&port.port_code).bind(&month).execute(&mut **tx).await?.last_insert_id();
    // This is the payment/prepayment amount, not a final metered charging fee.
    let payment_id = sqlx::query(
        "INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,status,created_month,expired_at) \
         VALUES (?,'charge',?,?,'wechat',?,'initiated',?,?)"
    ).bind(payment_no).bind(order_id).bind(user_id).bind(quote.total_cents)
        .bind(&month).bind(expires_at).execute(&mut **tx).await?.last_insert_id();
    sqlx::query("UPDATE charge_order SET payment_order_id=? WHERE id=? AND created_month=?")
        .bind(payment_id).bind(order_id).bind(&month).execute(&mut **tx).await?;
    crate::order_events::record(tx, order_id, "created", &format!("user:{user_id}"), "创建充电订单，等待支付").await?;
    Ok(order_id)
}

/// Locks payment before charge, matching the payment callback's lock order.
/// A repeated cancellation returns the port again so Redis cleanup can retry.
pub async fn cancel_pending(tx: &mut Transaction<'_, MySql>, user_id: u64, order_no: &str) -> AppResult<String> {
    let identity: Option<(u64, Option<u64>)> = sqlx::query_as(
        "SELECT id,payment_order_id FROM charge_order WHERE order_no=? AND user_id=? AND deleted_at IS NULL"
    ).bind(order_no).bind(user_id).fetch_optional(&mut **tx).await?;
    let (id, payment_id) = identity.ok_or_else(|| AppError::NotFound("订单不存在".into()))?;
    let payment_id = payment_id.ok_or_else(|| AppError::Conflict("订单缺少支付单，请联系客服".into()))?;
    let payment: Option<(String, u64)> = sqlx::query_as(
        "SELECT status,biz_id FROM payment_order WHERE id=? AND user_id=? AND biz_type='charge' AND deleted_at IS NULL FOR UPDATE"
    ).bind(payment_id).bind(user_id).fetch_optional(&mut **tx).await?;
    let (payment_status, biz_id) = payment.ok_or_else(|| AppError::Conflict("支付单不存在".into()))?;
    let charge: Option<(String, String, Option<u64>, bool)> = sqlx::query_as(
        "SELECT status,port_code,payment_order_id,(created_at > UTC_TIMESTAMP(3)-INTERVAL 60 SECOND) \
         FROM charge_order WHERE id=? AND user_id=? AND deleted_at IS NULL FOR UPDATE"
    ).bind(id).bind(user_id).fetch_optional(&mut **tx).await?;
    let (status, port_code, linked_payment, in_window) = charge.ok_or_else(|| AppError::NotFound("订单不存在".into()))?;
    if biz_id != id || linked_payment != Some(payment_id) {
        return Err(AppError::Conflict("订单与支付单关联不一致".into()));
    }
    if status == "cancelled" && payment_status == "closed" { return Ok(port_code); }
    if status != "pending_payment" || payment_status != "initiated" {
        return Err(AppError::business(2018, "订单已支付或不处于待支付状态"));
    }
    if !in_window { return Err(AppError::business(2019, "已超过 60 秒取消窗口")); }
    sqlx::query("UPDATE charge_order SET status='cancelled',ended_at=UTC_TIMESTAMP(3) WHERE id=?")
        .bind(id).execute(&mut **tx).await?;
    sqlx::query("UPDATE payment_order SET status='closed',closed_at=UTC_TIMESTAMP(3) WHERE id=?")
        .bind(payment_id).execute(&mut **tx).await?;
    crate::order_events::record(tx,id,"cancelled",&format!("user:{user_id}"),"用户取消待支付订单").await?;
    Ok(port_code)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_inconsistent_or_unpayable_quotes() {
        for (total, electric, service) in [(0,0,0), (-1,0,-1), (10,5,6), (10,-1,11),
            (i64::MAX,i64::MAX,1), (i32::MAX as i64+1,i32::MAX as i64+1,0)] {
            assert!(validate_quote(&QuoteResponse {total_cents:total,electric_cents:electric,service_cents:service}).is_err());
        }
        assert_eq!(validate_quote(&QuoteResponse {total_cents:123,electric_cents:100,service_cents:23}).unwrap(),123);
    }

    #[tokio::test]
    #[ignore = "requires the migrated development user database"]
    async fn persists_verified_identity_amount_and_event_atomically() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").expect("DATABASE_URL")).await.unwrap();
        let mut tx = pool.begin().await.unwrap();
        let order = format!("checkout_test_{}", uuid::Uuid::new_v4().simple());
        let payment = format!("pay_{}", uuid::Uuid::new_v4().simple());
        let port = ScanPortDetail {port_id:"test-device:7".into(),port_code:"test-device:7".into(),device_id:"test-device".into(),port_no:7,status:"idle".into()};
        let quote = QuoteResponse {total_cents:1234,electric_cents:1000,service_cents:234};
        let id = persist_pending(&mut tx, 123, &order, &payment, &port, &quote, Utc::now()+chrono::Duration::seconds(300)).await.unwrap();
        let row: (String,u8,String,i64,u64,Option<i64>) = sqlx::query_as(
            "SELECT c.device_id,c.port_no,c.port_code,p.total_cents,p.biz_id,c.total_cents \
             FROM charge_order c JOIN payment_order p ON p.id=c.payment_order_id WHERE c.id=?"
        ).bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(row, ("test-device".into(),7,"test-device:7".into(),1234,id,None));
        let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM charge_event_log WHERE charge_order_id=?")
            .bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(count, 1);
        assert!(matches!(cancel_pending(&mut tx, 456, &order).await, Err(AppError::NotFound(_))));
        sqlx::query("UPDATE payment_order SET status='paid' WHERE order_no=?")
            .bind(&payment).execute(&mut *tx).await.unwrap();
        assert_eq!(cancel_pending(&mut tx, 123, &order).await.unwrap_err().code(), 2018);
        sqlx::query("UPDATE payment_order SET status='initiated' WHERE order_no=?")
            .bind(&payment).execute(&mut *tx).await.unwrap();
        sqlx::query("UPDATE charge_order SET created_at=UTC_TIMESTAMP()-INTERVAL 61 SECOND WHERE id=?")
            .bind(id).execute(&mut *tx).await.unwrap();
        assert_eq!(cancel_pending(&mut tx, 123, &order).await.unwrap_err().code(), 2019);
        sqlx::query("UPDATE charge_order SET created_at=UTC_TIMESTAMP() WHERE id=?")
            .bind(id).execute(&mut *tx).await.unwrap();
        assert_eq!(cancel_pending(&mut tx, 123, &order).await.unwrap(), port.port_code);
        assert_eq!(cancel_pending(&mut tx, 123, &order).await.unwrap(), port.port_code);
        let states: (String, String) = sqlx::query_as(
            "SELECT c.status,p.status FROM charge_order c JOIN payment_order p ON p.id=c.payment_order_id WHERE c.id=?"
        ).bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(states, ("cancelled".into(),"closed".into()));
        let cancelled: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM charge_event_log WHERE charge_order_id=? AND event='cancelled'")
            .bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(cancelled, 1);
        tx.rollback().await.unwrap();
        let remaining: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM payment_order WHERE order_no=?")
            .bind(payment).fetch_one(&pool).await.unwrap();
        assert_eq!(remaining, 0);
    }
}
