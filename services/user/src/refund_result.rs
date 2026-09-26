//! Payment first, refund second: serialize terminal refund accounting with reservations.
use crate::refund::ResultReq;
use common_error::{AppError, AppResult};
use sqlx::Row;
fn conflict() -> AppError {
    AppError::Conflict("退款结果与支付订单或已保存结果不一致".into())
}
pub async fn callback(
    axum::extract::State(st): axum::extract::State<crate::AppState>,
    headers: axum::http::HeaderMap,
    body: axum::body::Bytes,
) -> AppResult<axum::http::StatusCode> {
    let cfg = st
        .cfg
        .wechat
        .as_ref()
        .ok_or_else(|| AppError::Config("缺少微信支付配置".into()))?;
    let notice = common_wechat::decode_refund_notification(cfg, &headers, &body)?;
    let mut tx = st.db.pool().begin().await?;
    apply_notification(&mut tx, &notice).await?;
    tx.commit().await?;
    Ok(axum::http::StatusCode::NO_CONTENT)
}
pub async fn apply_notification(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    n: &common_wechat::RefundNotification,
) -> AppResult<()> {
    let ids: Vec<(u64, u64)> = sqlx::query_as(
        "SELECT id,payment_order_id FROM refund_record WHERE refund_no=? AND deleted_at IS NULL",
    )
    .bind(&n.out_refund_no)
    .fetch_all(&mut **tx)
    .await?;
    if ids.len() != 1 {
        return Err(conflict());
    }
    let (rid, pid) = ids[0];
    let payments=sqlx::query("SELECT order_no,wechat_transaction_id,total_cents,pay_method FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    if payments.len() != 1 {
        return Err(conflict());
    }
    let pay = &payments[0];
    if pay.try_get::<String, _>("order_no")? != n.out_trade_no
        || pay
            .try_get::<Option<String>, _>("wechat_transaction_id")?
            .as_deref()
            != Some(n.transaction_id.as_str())
        || pay.try_get::<i64, _>("total_cents")? != n.amount.total
        || pay.try_get::<String, _>("pay_method")? != "wechat"
    {
        return Err(conflict());
    }
    let rows:Vec<(i64,String,Option<String>)>=sqlx::query_as("SELECT refund_cents,status,wechat_refund_id FROM refund_record WHERE id=? AND payment_order_id=? AND deleted_at IS NULL FOR UPDATE").bind(rid).bind(pid).fetch_all(&mut **tx).await?;
    if rows.len() != 1 || rows[0].0 != n.amount.refund {
        return Err(conflict());
    }
    // A valid delayed failure notification cannot undo an already confirmed success.
    if rows[0].1 == "success" && n.refund_status != "SUCCESS" {
        return if rows[0].2.as_deref() == Some(n.refund_id.as_str()) {
            Ok(())
        } else {
            Err(conflict())
        };
    }
    apply(
        tx,
        &n.out_refund_no,
        &ResultReq {
            refund_no: n.out_refund_no.clone(),
            success: n.refund_status == "SUCCESS",
            wechat_refund_id: Some(n.refund_id.clone()),
            failure_reason: if n.refund_status == "SUCCESS" {
                None
            } else {
                Some(format!("微信退款状态 {}，需人工处理", n.refund_status))
            },
        },
    )
    .await
}
pub async fn apply(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    path: &str,
    req: &ResultReq,
) -> AppResult<()> {
    if path != req.refund_no
        || path.is_empty()
        || path.len() > 64
        || req
            .failure_reason
            .as_ref()
            .is_some_and(|v| v.chars().count() > 255)
        || (req.success
            && req.wechat_refund_id.as_deref().map_or(true, |v| {
                v.is_empty() || v.len() > 64 || !v.bytes().all(|c| c.is_ascii_alphanumeric())
            }))
    {
        return Err(conflict());
    }
    let ids: Vec<(u64, u64)> = sqlx::query_as(
        "SELECT id,payment_order_id FROM refund_record WHERE refund_no=? AND deleted_at IS NULL",
    )
    .bind(path)
    .fetch_all(&mut **tx)
    .await?;
    if ids.len() != 1 {
        return Err(conflict());
    }
    let (rid, pid) = ids[0];
    let payments=sqlx::query("SELECT user_id,biz_id,biz_type,paid_cents,refunded_cents,status FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    if payments.len() != 1 {
        return Err(conflict());
    }
    let pay = &payments[0];
    let rows=sqlx::query("SELECT id,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,wechat_refund_id FROM refund_record WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(rid).fetch_all(&mut **tx).await?;
    if rows.len() != 1 {
        return Err(conflict());
    }
    let row = &rows[0];
    if row.try_get::<u64, _>("payment_order_id")? != pid
        || row.try_get::<u64, _>("user_id")? != pay.try_get::<u64, _>("user_id")?
        || row.try_get::<String, _>("biz_type")? != pay.try_get::<String, _>("biz_type")?
        || row.try_get::<u64, _>("biz_id")? != pay.try_get::<u64, _>("biz_id")?
    {
        return Err(conflict());
    }
    let status: String = row.try_get("status")?;
    if status == "success" {
        return if req.success
            && row.try_get::<Option<String>, _>("wechat_refund_id")? == req.wechat_refund_id
        {
            Ok(())
        } else {
            Err(conflict())
        };
    }
    if !["processing", "failed"].contains(&status.as_str()) {
        return Err(conflict());
    }
    if !req.success && status == "failed" {
        return Ok(());
    }
    if req.success {
        let cents: i64 = row.try_get("refund_cents")?;
        if row.try_get::<String, _>("biz_type")? == "wallet_recharge" {
            let part:Option<(u64,i64,bool)>=sqlx::query_as("SELECT wallet_account_id,amount_cents,settled FROM wallet_refund_part WHERE refund_record_id=? FOR UPDATE").bind(rid).fetch_optional(&mut **tx).await?;
            let (wid, amount, settled) = part.ok_or_else(conflict)?;
            if amount != cents || settled {
                return Err(conflict());
            }
            let wallets:Vec<(i64,i64)>=sqlx::query_as("SELECT balance_cents,frozen_cents FROM wallet_account WHERE id=? AND user_id=? AND deleted_at IS NULL FOR UPDATE").bind(wid).bind(row.try_get::<u64,_>("user_id")?).fetch_all(&mut **tx).await?;
            if wallets.len() != 1 || wallets[0].1 < cents {
                return Err(conflict());
            }
            sqlx::query("UPDATE wallet_account SET frozen_cents=frozen_cents-?,version=version+1 WHERE id=?").bind(cents).bind(wid).execute(&mut **tx).await?;
            sqlx::query("UPDATE wallet_refund_part SET settled=1 WHERE refund_record_id=?")
                .bind(rid)
                .execute(&mut **tx)
                .await?;
            sqlx::query("INSERT INTO wallet_txn (txn_no,user_id,wallet_account_id,direction,amount_cents,balance_after_cents,biz_type,biz_ref,note,created_month) VALUES (?,?,?,'out',?,?,'refund',?,'原路退款成功，扣除已预留资金',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(common_db::IdGen::new("WTX").next()).bind(row.try_get::<u64,_>("user_id")?).bind(wid).bind(cents).bind(wallets[0].0).bind(path).execute(&mut **tx).await?;
        }
        let paid: i64 = pay.try_get("paid_cents")?;
        let refunded: i64 = pay.try_get("refunded_cents")?;
        let total = refunded.checked_add(cents).ok_or_else(conflict)?;
        if cents <= 0
            || refunded < 0
            || total > paid
            || !["paid", "partial_refunded"].contains(&pay.try_get::<String, _>("status")?.as_str())
        {
            return Err(conflict());
        }
        sqlx::query(
            "INSERT INTO refund_success_receipt (refund_record_id,wechat_refund_id) VALUES (?,?)",
        )
        .bind(rid)
        .bind(&req.wechat_refund_id)
        .execute(&mut **tx)
        .await?;
        sqlx::query("UPDATE refund_record SET status='success',wechat_refund_id=?,completed_at=UTC_TIMESTAMP(3),failure_reason=NULL WHERE id=?").bind(&req.wechat_refund_id).bind(rid).execute(&mut **tx).await?;
        sqlx::query("UPDATE payment_order SET refunded_cents=?,status=? WHERE id=?")
            .bind(total)
            .bind(if total == paid {
                "refunded"
            } else {
                "partial_refunded"
            })
            .bind(pid)
            .execute(&mut **tx)
            .await?;
    } else {
        sqlx::query("UPDATE refund_record SET status='failed',failure_reason=?,retry_count=retry_count+1 WHERE id=?").bind(&req.failure_reason).bind(rid).execute(&mut **tx).await?;
    }
    let event = common_redis::StreamEnvelope::new(
        "refund_completed",
        "user",
        serde_json::json!({"refund_no":path,"success":req.success}),
    );
    sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
        .bind(&event.event_id)
        .bind(common_redis::streams::COMP_TX)
        .bind(serde_json::to_value(&event)?)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires development user MySQL"]
    async fn callback_and_query_race_credits_once_and_rejects_wrong_amount() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let tag = format!("CB_{}", uuid::Uuid::new_v4().simple());
        let provider = format!("500{}", chrono::Utc::now().timestamp_micros());
        let mut tx = pool.begin().await.unwrap();
        let pid=sqlx::query("INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month) VALUES (?,'charge',999999,123,'wechat',100,100,'paid',?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(&tag).bind(&tag).execute(&mut *tx).await.unwrap().last_insert_id();
        let rid=sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,created_month) VALUES (?,?,123,'charge',999999,82,'processing',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(&tag).bind(pid).execute(&mut *tx).await.unwrap().last_insert_id();
        tx.commit().await.unwrap();
        let n:common_wechat::RefundNotification=serde_json::from_value(serde_json::json!({"mchid":"test","out_trade_no":tag,"transaction_id":tag,"out_refund_no":tag,"refund_id":provider,"refund_status":"SUCCESS","success_time":chrono::Utc::now().to_rfc3339(),"amount":{"total":100,"refund":82,"payer_total":100,"payer_refund":82}})).unwrap();
        let mut jobs = Vec::new();
        for index in 0..6 {
            let p = pool.clone();
            let n = n.clone();
            jobs.push(tokio::spawn(async move {
                let mut tx = p.begin().await?;
                if index % 2 == 0 {
                    apply_notification(&mut tx, &n).await?;
                } else {
                    apply(
                        &mut tx,
                        &n.out_refund_no,
                        &ResultReq {
                            refund_no: n.out_refund_no.clone(),
                            success: true,
                            wechat_refund_id: Some(n.refund_id.clone()),
                            failure_reason: None,
                        },
                    )
                    .await?;
                }
                tx.commit().await?;
                Ok::<_, AppError>(())
            }));
        }
        let mut results = Vec::new();
        for job in jobs {
            results.push(job.await.unwrap());
        }
        let actual: (i64, String) =
            sqlx::query_as("SELECT refunded_cents,status FROM payment_order WHERE id=?")
                .bind(pid)
                .fetch_one(&pool)
                .await
                .unwrap();
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))=?").bind(&tag).fetch_one(&pool).await.unwrap();
        let mut tx = pool.begin().await.unwrap();
        let mut wrong = n.clone();
        wrong.amount.refund = 83;
        let wrong_rejected = apply_notification(&mut tx, &wrong).await.is_err();
        tx.rollback().await.unwrap();
        let mut tx = pool.begin().await.unwrap();
        let mut late = n.clone();
        late.refund_status = "ABNORMAL".into();
        let late_accepted = apply_notification(&mut tx, &late).await.is_ok();
        tx.rollback().await.unwrap();
        let mut tx = pool.begin().await.unwrap();
        sqlx::query("DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))=?").bind(&tag).execute(&mut *tx).await.unwrap();
        sqlx::query("DELETE FROM refund_success_receipt WHERE refund_record_id=?")
            .bind(rid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("DELETE FROM refund_record WHERE id=?")
            .bind(rid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("DELETE FROM payment_order WHERE id=?")
            .bind(pid)
            .execute(&mut *tx)
            .await
            .unwrap();
        tx.commit().await.unwrap();
        for result in results {
            result.unwrap();
        }
        assert_eq!(actual, (82, "partial_refunded".into()));
        assert_eq!(count, 1);
        assert!(wrong_rejected);
        assert!(late_accepted);
    }
}
