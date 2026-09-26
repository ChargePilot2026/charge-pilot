//! A verified WeChat receipt and all business effects commit together.
use chrono::{NaiveDate, NaiveDateTime, Utc};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use common_wechat::PaymentNotification;
use serde_json::json;
use sha2::{Digest, Sha256};
use sqlx::{MySql, Row, Transaction};

fn conflict() -> AppError {
    AppError::Conflict("支付通知与订单记录不一致".into())
}

pub async fn process(pool: &sqlx::MySqlPool, n: &PaymentNotification) -> AppResult<()> {
    for attempt in 0..3 {
        let mut tx = pool.begin().await?;
        match record(&mut tx, n).await {
            Ok(()) => {
                tx.commit().await?;
                return Ok(());
            }
            Err(err) => {
                let deadlock = matches!(&err,AppError::Database(sqlx::Error::Database(db)) if db.code().as_deref()==Some("40001"));
                tx.rollback().await?;
                if !deadlock || attempt == 2 {
                    return Err(err);
                }
                tokio::time::sleep(std::time::Duration::from_millis(25 * (attempt + 1))).await;
            }
        }
    }
    unreachable!()
}

pub async fn record(tx: &mut Transaction<'_, MySql>, n: &PaymentNotification) -> AppResult<()> {
    // Same lock order as cancellation: payment -> charge. Never trust attach.
    let rows = sqlx::query("SELECT id,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month,expired_at FROM payment_order WHERE order_no=? AND deleted_at IS NULL FOR UPDATE")
        .bind(&n.out_trade_no).fetch_all(&mut **tx).await?;
    if rows.len() != 1 {
        return Err(conflict());
    }
    let p = &rows[0];
    let id: u64 = p.try_get("id")?;
    let uid: u64 = p.try_get("user_id")?;
    let biz_id: u64 = p.try_get("biz_id")?;
    let biz: String = p.try_get("biz_type")?;
    let status: String = p.try_get("status")?;
    let month: NaiveDate = p.try_get("created_month")?;
    let total: i64 = p.try_get("total_cents")?;
    if p.try_get::<String, _>("pay_method")? != "wechat" || total != n.amount.total {
        return Err(conflict());
    }
    let owner: Option<(String, String)> = sqlx::query_as(
        "SELECT openid,status FROM user WHERE id=? AND deleted_at IS NULL FOR UPDATE",
    )
    .bind(uid)
    .fetch_optional(&mut **tx)
    .await?;
    let (openid, user_status) = owner.ok_or_else(conflict)?;
    if openid != n.payer.openid {
        return Err(conflict());
    }
    // Stable payment identity, independent of notification ID, JSON whitespace and retries.
    let digest = format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(&json!([
            id,
            month,
            n.appid,
            n.mchid,
            n.out_trade_no,
            n.transaction_id,
            n.payer.openid,
            n.amount.total,
            n.amount.payer_total,
            n.success_time.timestamp_millis()
        ]))?)
    );
    let inserted = sqlx::query("INSERT IGNORE INTO payment_callback_idempotent (wechat_transaction_id,request_digest) VALUES (?,?)")
        .bind(&n.transaction_id).bind(&digest).execute(&mut **tx).await?.rows_affected() == 1;
    let recorded: Option<String> = sqlx::query_scalar("SELECT request_digest FROM payment_callback_idempotent WHERE wechat_transaction_id=? FOR UPDATE")
        .bind(&n.transaction_id).fetch_one(&mut **tx).await?;
    if recorded.as_deref() != Some(digest.as_str()) {
        return Err(conflict());
    }
    let previous: Option<String> = p.try_get("wechat_transaction_id")?;
    if !inserted {
        if previous.as_deref() == Some(n.transaction_id.as_str())
            && p.try_get::<i64, _>("paid_cents")? == total
            && matches!(status.as_str(), "paid" | "partial_refunded" | "refunded")
        {
            return Ok(());
        }
        return Err(conflict());
    }
    if previous.is_some()
        || !matches!(status.as_str(), "initiated" | "closed" | "failed")
        || p.try_get::<i64, _>("paid_cents")? != 0
    {
        return Err(conflict());
    }

    let mut refund = status != "initiated";
    let mut charge = None;
    if biz == "charge" {
        let rows = sqlx::query("SELECT id,order_no,user_id,device_id,port_no,port_code,payment_order_id,status FROM charge_order WHERE id=? AND deleted_at IS NULL FOR UPDATE")
            .bind(biz_id).fetch_all(&mut **tx).await?;
        if rows.len() != 1 {
            return Err(conflict());
        }
        let c = rows.into_iter().next().unwrap();
        if c.try_get::<u64, _>("user_id")? != uid
            || c.try_get::<Option<u64>, _>("payment_order_id")? != Some(id)
        {
            return Err(conflict());
        }
        let charge_status: String = c.try_get("status")?;
        if !matches!(
            charge_status.as_str(),
            "pending_payment" | "cancelled" | "failed"
        ) {
            return Err(conflict());
        }
        let expiry: Option<NaiveDateTime> = p.try_get("expired_at")?;
        refund |= charge_status != "pending_payment"
            || expiry.map_or(true, |t| t <= Utc::now().naive_utc())
            || user_status != "active";
        charge = Some(c);
    } else if biz != "wallet_recharge" {
        return Err(conflict());
    }

    // total is the settled order face value; payer_total may include WeChat-funded discounts.
    sqlx::query("UPDATE payment_order SET status='paid',paid_cents=?,wechat_transaction_id=?,paid_at=? WHERE id=? AND created_month=?")
        .bind(total).bind(&n.transaction_id).bind(n.success_time.naive_utc()).bind(id).bind(month).execute(&mut **tx).await?;
    if refund {
        let refund_no = IdGen::new("REF").next();
        sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES (?,?,?,?,?,?,'订单关闭或无法启动后收到付款','pending',?)")
            .bind(&refund_no).bind(id).bind(uid).bind(&biz).bind(biz_id).bind(total).bind(Utc::now().format("%Y-%m-01").to_string()).execute(&mut **tx).await?;
        if let Some(c) = charge.as_ref() {
            if c.try_get::<String, _>("status")? == "pending_payment" {
                sqlx::query("UPDATE charge_order SET status='cancelled',ended_at=UTC_TIMESTAMP(3),failure_reason='付款确认时订单已失效，等待退款' WHERE id=?")
                    .bind(biz_id).execute(&mut **tx).await?;
            }
            crate::order_events::record(
                tx,
                biz_id,
                "refund_required",
                "payment",
                "收到付款，订单无法启动，已创建退款记录",
            )
            .await?;
        }
        enqueue(tx,common_redis::streams::REFUND_REQUIRED,StreamEnvelope::new("refund_required","user",json!({"refund_no":refund_no,"payment_order_id":id,"user_id":uid,"biz_type":biz,"biz_id":biz_id,"refund_cents":total}))).await?;
    } else if let Some(c) = charge {
        sqlx::query("UPDATE charge_order SET status='paid' WHERE id=?")
            .bind(biz_id)
            .execute(&mut **tx)
            .await?;
        crate::order_events::record(tx, biz_id, "paid", "payment", "支付确认").await?;
        enqueue(tx,common_redis::streams::CHARGE_STARTED,StreamEnvelope::new("charge_started","user",json!({
            "charge_order_id":biz_id,"payment_order_id":id,"order_no":c.try_get::<String,_>("order_no")?,
            "user_id":uid,"device_id":c.try_get::<String,_>("device_id")?,"port_no":c.try_get::<u8,_>("port_no")?,"port_code":c.try_get::<Option<String>,_>("port_code")?
        }))).await?;
    } else {
        // The user row lock serializes first-wallet creation despite NULL soft-delete uniqueness.
        let wallets: Vec<(u64,i64)>=sqlx::query_as("SELECT id,balance_cents FROM wallet_account WHERE user_id=? AND deleted_at IS NULL FOR UPDATE")
            .bind(uid).fetch_all(&mut **tx).await?;
        if wallets.len() > 1 {
            return Err(conflict());
        }
        let (wid, balance) = match wallets.first() {
            Some(v) => *v,
            None => (
                sqlx::query("INSERT INTO wallet_account (user_id,status) VALUES (?,?)")
                    .bind(uid)
                    .bind(&user_status)
                    .execute(&mut **tx)
                    .await?
                    .last_insert_id(),
                0,
            ),
        };
        let balance = balance.checked_add(total).ok_or_else(conflict)?;
        sqlx::query("UPDATE wallet_account SET balance_cents=?,version=version+1 WHERE id=?")
            .bind(balance)
            .bind(wid)
            .execute(&mut **tx)
            .await?;
        let txn=sqlx::query("INSERT INTO wallet_txn (txn_no,user_id,wallet_account_id,direction,amount_cents,balance_after_cents,biz_type,biz_ref,created_month) VALUES (?,?,?,'in',?,?,'recharge',?,?)")
            .bind(IdGen::new("WTX").next()).bind(uid).bind(wid).bind(total).bind(balance).bind(&n.out_trade_no).bind(Utc::now().format("%Y-%m-01").to_string()).execute(&mut **tx).await?.last_insert_id();
        sqlx::query("UPDATE payment_order SET biz_id=? WHERE id=? AND created_month=?")
            .bind(txn)
            .bind(id)
            .bind(month)
            .execute(&mut **tx)
            .await?;
    }
    Ok(())
}

async fn enqueue(
    tx: &mut Transaction<'_, MySql>,
    stream: &str,
    envelope: StreamEnvelope,
) -> AppResult<()> {
    sqlx::query(
        "INSERT INTO event_outbox (event_id,stream,envelope_json,status) VALUES (?,?,?,'pending')",
    )
    .bind(&envelope.event_id)
    .bind(stream)
    .bind(serde_json::to_string(&envelope)?)
    .execute(&mut **tx)
    .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use sqlx::Executor;

    async fn fixture(
        tx: &mut Transaction<'_, MySql>,
        charge: bool,
    ) -> (u64, u64, PaymentNotification) {
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let openid = format!("receipt_{tag}");
        let uid = sqlx::query("INSERT INTO user (openid) VALUES (?)")
            .bind(&openid)
            .execute(&mut **tx)
            .await
            .unwrap()
            .last_insert_id();
        let no = format!("PAY_{tag}");
        let cid = if charge {
            crate::checkout::persist_pending(
                tx,
                uid,
                &format!("ORD_{tag}"),
                &no,
                &api_contracts::ScanPortDetail {
                    port_id: tag.clone(),
                    port_code: tag.clone(),
                    device_id: "RECEIPT_TEST".into(),
                    port_no: 1,
                    status: "idle".into(),
                },
                &api_contracts::QuoteResponse {
                    total_cents: 100,
                    electric_cents: 80,
                    service_cents: 20,
                },
                Utc::now() + chrono::Duration::minutes(5),
            )
            .await
            .unwrap()
        } else {
            sqlx::query("INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,created_month) VALUES (?,'wallet_recharge',0,?,'wechat',100,?)")
                .bind(&no).bind(uid).bind(Utc::now().format("%Y-%m-01").to_string()).execute(&mut **tx).await.unwrap();
            0
        };
        let n=serde_json::from_value(json!({"appid":"wx_test","mchid":"1900000109","out_trade_no":no,"transaction_id":format!("WX_{tag}"),"trade_type":"JSAPI","trade_state":"SUCCESS","success_time":Utc::now().to_rfc3339(),"payer":{"openid":openid},"amount":{"total":100,"payer_total":90,"currency":"CNY","payer_currency":"CNY"}})).unwrap();
        (uid, cid, n)
    }

    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn receipt_atomicity_replay_late_payment_and_wallet() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let (uid, cid, n) = fixture(&mut tx, true).await;
        let before: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM event_outbox")
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        for wrong in ["amount", "owner"] {
            tx.execute("SAVEPOINT invalid_receipt").await.unwrap();
            let mut bad = n.clone();
            if wrong == "amount" {
                bad.amount.total = 101;
            } else {
                bad.payer.openid = "other_user".into();
            }
            assert!(record(&mut tx, &bad).await.is_err());
            tx.execute("ROLLBACK TO SAVEPOINT invalid_receipt")
                .await
                .unwrap();
        }
        record(&mut tx, &n).await.unwrap();
        record(&mut tx, &n).await.unwrap();
        let status: String = sqlx::query_scalar("SELECT status FROM charge_order WHERE id=?")
            .bind(cid)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(status, "paid");
        let paid: i64 = sqlx::query_scalar("SELECT paid_cents FROM payment_order WHERE order_no=?")
            .bind(&n.out_trade_no)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(paid, 100);
        let events:Vec<(String,serde_json::Value)>=sqlx::query_as("SELECT event_id,envelope_json FROM event_outbox WHERE JSON_EXTRACT(envelope_json,'$.payload.charge_order_id')=?")
            .bind(cid).fetch_all(&mut *tx).await.unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].1["event_id"], events[0].0);
        assert!(events[0].1["payload"]["order_no"]
            .as_str()
            .unwrap()
            .starts_with("ORD_"));
        tx.execute("SAVEPOINT second_transaction").await.unwrap();
        let mut second = n.clone();
        second.transaction_id.push('2');
        assert!(record(&mut tx, &second).await.is_err());
        tx.execute("ROLLBACK TO SAVEPOINT second_transaction")
            .await
            .unwrap();
        // One provider transaction cannot pay a different order, even for the same amount.
        let (_, _, mut reused) = fixture(&mut tx, false).await;
        reused.transaction_id = n.transaction_id.clone();
        tx.execute("SAVEPOINT reused_transaction").await.unwrap();
        assert!(record(&mut tx, &reused).await.is_err());
        tx.execute("ROLLBACK TO SAVEPOINT reused_transaction")
            .await
            .unwrap();
        // A delayed duplicate never resets an already-refunded payment back to paid.
        sqlx::query(
            "UPDATE payment_order SET status='refunded',refunded_cents=100 WHERE order_no=?",
        )
        .bind(&n.out_trade_no)
        .execute(&mut *tx)
        .await
        .unwrap();
        record(&mut tx, &n).await.unwrap();
        let status: String =
            sqlx::query_scalar("SELECT status FROM payment_order WHERE order_no=?")
                .bind(&n.out_trade_no)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(status, "refunded");
        let (_, late_cid, late) = fixture(&mut tx, true).await;
        let pid: u64 = sqlx::query_scalar("SELECT id FROM payment_order WHERE order_no=?")
            .bind(&late.out_trade_no)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        sqlx::query("UPDATE charge_order SET status='cancelled' WHERE id=?")
            .bind(late_cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("UPDATE payment_order SET status='closed' WHERE id=?")
            .bind(pid)
            .execute(&mut *tx)
            .await
            .unwrap();
        record(&mut tx, &late).await.unwrap();
        record(&mut tx, &late).await.unwrap();
        let status: String = sqlx::query_scalar("SELECT status FROM charge_order WHERE id=?")
            .bind(late_cid)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(status, "cancelled");
        let refunds: i64 = sqlx::query_scalar(
            "SELECT COUNT(*) FROM refund_record WHERE payment_order_id=? AND refund_cents=100",
        )
        .bind(pid)
        .fetch_one(&mut *tx)
        .await
        .unwrap();
        assert_eq!(refunds, 1);
        let after: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM event_outbox")
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(after, before + 2);
        let (wallet_uid, _, recharge) = fixture(&mut tx, false).await;
        record(&mut tx, &recharge).await.unwrap();
        record(&mut tx, &recharge).await.unwrap();
        let balance: i64 =
            sqlx::query_scalar("SELECT balance_cents FROM wallet_account WHERE user_id=?")
                .bind(wallet_uid)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(balance, 100);
        let ledger: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM wallet_txn WHERE user_id=?")
            .bind(wallet_uid)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(ledger, 1);
        tx.rollback().await.unwrap();
        let exists: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM user WHERE id IN (?,?)")
            .bind(uid)
            .bind(wallet_uid)
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(exists, 0);
    }

    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn concurrent_recharge_notifications_credit_once() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let (uid, _, n) = fixture(&mut tx, false).await;
        tx.commit().await.unwrap();
        let mut jobs = Vec::new();
        for _ in 0..5 {
            let pool = pool.clone();
            let n = n.clone();
            jobs.push(tokio::spawn(async move { process(&pool, &n).await }));
        }
        let mut results = Vec::new();
        for job in jobs {
            results.push(job.await);
        }
        let balance: Result<i64, _> =
            sqlx::query_scalar("SELECT balance_cents FROM wallet_account WHERE user_id=?")
                .bind(uid)
                .fetch_one(&pool)
                .await;
        let ledger: Result<i64, _> =
            sqlx::query_scalar("SELECT COUNT(*) FROM wallet_txn WHERE user_id=?")
                .bind(uid)
                .fetch_one(&pool)
                .await;
        // Clean fixtures before assertions, including when an individual concurrent call failed.
        sqlx::query("DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id=?")
            .bind(&n.transaction_id)
            .execute(&pool)
            .await
            .unwrap();
        for table in ["wallet_txn", "wallet_account", "payment_order"] {
            sqlx::query(&format!("DELETE FROM {table} WHERE user_id=?"))
                .bind(uid)
                .execute(&pool)
                .await
                .unwrap();
        }
        sqlx::query("DELETE FROM user WHERE id=?")
            .bind(uid)
            .execute(&pool)
            .await
            .unwrap();
        for result in results {
            result.unwrap().unwrap();
        }
        assert_eq!(balance.unwrap(), 100);
        assert_eq!(ledger.unwrap(), 1);
    }
}
