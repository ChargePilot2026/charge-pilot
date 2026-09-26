//! Automatic charge refunds only; manual/unsupported refunds stay available for review.
use crate::AppState;
use api_contracts::refunds::{ExecutionDetail, ExecutionRequest};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;
fn conflict() -> AppError {
    AppError::Conflict("退款不满足自动执行条件，需财务审核".into())
}
pub async fn prepare(
    State(st): State<AppState>,
    Json(req): Json<ExecutionRequest>,
) -> AppResult<Json<ApiEnvelope<ExecutionDetail>>> {
    let mut tx = st.db.pool().begin().await?;
    let ids: Vec<(u64, u64)> = sqlx::query_as(
        "SELECT id,payment_order_id FROM refund_record WHERE refund_no=? AND deleted_at IS NULL",
    )
    .bind(&req.refund_no)
    .fetch_all(&mut *tx)
    .await?;
    if ids.len() != 1 {
        return Err(conflict());
    }
    let (rid, pid) = ids[0];
    let pays=sqlx::query("SELECT user_id,biz_id,biz_type,pay_method,total_cents,paid_cents,refunded_cents,wechat_transaction_id,status FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut *tx).await?;
    if pays.len() != 1 {
        return Err(conflict());
    }
    let pay = &pays[0];
    let refunds=sqlx::query("SELECT id,user_id,biz_id,biz_type,refund_cents,status,reason,claimed_by,wechat_refund_id FROM refund_record WHERE payment_order_id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut *tx).await?;
    let row = refunds
        .iter()
        .find(|r| r.try_get::<u64, _>("id").ok() == Some(rid))
        .ok_or_else(conflict)?;
    let uid: u64 = pay.try_get("user_id")?;
    let cid: u64 = pay.try_get("biz_id")?;
    let biz_type = pay.try_get::<String, _>("biz_type")?;
    if row.try_get::<u64, _>("user_id")? != uid
        || row.try_get::<u64, _>("biz_id")? != cid
        || row.try_get::<String, _>("biz_type")? != biz_type
        || !["charge", "wallet_recharge"].contains(&biz_type.as_str())
        || pay.try_get::<String, _>("pay_method")? != "wechat"
        || row.try_get::<Option<u64>, _>("claimed_by")?.is_some()
    {
        return Err(conflict());
    }
    let status: String = row.try_get("status")?;
    let claimed: Option<u64> = sqlx::query_scalar(
        "SELECT refund_record_id FROM refund_execution WHERE refund_record_id=? FOR UPDATE",
    )
    .bind(rid)
    .fetch_optional(&mut *tx)
    .await?;
    if claimed.is_none() {
        if status != "pending" {
            return Err(conflict());
        }
        let review=sqlx::query("SELECT first_signer,second_signer,snapshot_json FROM refund_review WHERE refund_record_id=? FOR UPDATE").bind(rid).fetch_optional(&mut *tx).await?;
        let mut reviewed=false;
        if let Some(review)=review {
            let second:Option<u64>=review.try_get("second_signer")?;
            if second.is_none() || second==Some(review.try_get::<u64,_>("first_signer")?){return Err(conflict());}
            let expected=serde_json::json!({"refund_no":req.refund_no,"payment_order_id":pid,"user_id":uid,"charge_order_id":cid,"refund_cents":row.try_get::<i64,_>("refund_cents")?,"reason":row.try_get::<Option<String>,_>("reason")?,"total_cents":pay.try_get::<i64,_>("total_cents")?,"paid_cents":pay.try_get::<i64,_>("paid_cents")?});
            if review.try_get::<serde_json::Value,_>("snapshot_json")?!=expected{return Err(conflict());}
            reviewed=true;
        }
        if biz_type == "wallet_recharge" {
            let permitted:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM wallet_refund_part p JOIN wallet_refund_request r ON r.request_id=p.request_id JOIN wallet_account w ON w.id=p.wallet_account_id WHERE p.refund_record_id=? AND p.amount_cents=? AND p.settled=0 AND r.user_id=? AND w.user_id=? AND w.status='active' AND w.deleted_at IS NULL)").bind(rid).bind(row.try_get::<i64,_>("refund_cents")?).bind(uid).bind(uid).fetch_one(&mut *tx).await?;
            if !permitted {
                return Err(conflict());
            }
        } else {
            let orders:Vec<(String,u64,Option<u64>)>=sqlx::query_as("SELECT status,user_id,payment_order_id FROM charge_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(cid).fetch_all(&mut *tx).await?;
            if orders.len() != 1 || orders[0].1 != uid || orders[0].2 != Some(pid) {
                return Err(conflict());
            }
            let reason: Option<String> = row.try_get("reason")?;
            let allowed = match reason.as_deref() {
                Some("设备启动失败") => orders[0].0 == "failed",
                Some("订单关闭或无法启动后收到付款") => {
                    ["cancelled", "failed"].contains(&orders[0].0.as_str())
                }
                Some("充电实结差额退款") => orders[0].0 == "completed"
                    && sqlx::query_scalar::<_, u64>(
                        "SELECT charge_order_id FROM charge_fee_receipt WHERE charge_order_id=?",
                    )
                    .bind(cid)
                    .fetch_optional(&mut *tx)
                    .await?
                    .is_some(),
                _ => false,
            };
            if !allowed && !(reviewed && ["completed","failed","cancelled"].contains(&orders[0].0.as_str())) {
                return Err(conflict());
            }
        }
    }
    if !["pending", "processing", "success", "failed"].contains(&status.as_str()) {
        return Err(conflict());
    }
    let paid: i64 = pay.try_get("paid_cents")?;
    let total: i64 = pay.try_get("total_cents")?;
    let refunded: i64 = pay.try_get("refunded_cents")?;
    if paid != total
        || paid <= 0
        || refunded < 0
        || refunded > paid
        || !["paid", "partial_refunded", "refunded"]
            .contains(&pay.try_get::<String, _>("status")?.as_str())
    {
        return Err(conflict());
    }
    let mut reserved = refunded;
    let mut successful = 0i64;
    for refund in &refunds {
        if refund.try_get::<String,_>("status")?=="rejected"{continue;}
        let amount: i64 = refund.try_get("refund_cents")?;
        if amount <= 0 {
            return Err(conflict());
        }
        if refund.try_get::<String, _>("status")? == "success" {
            successful = successful.checked_add(amount).ok_or_else(conflict)?;
        } else {
            reserved = reserved.checked_add(amount).ok_or_else(conflict)?;
        }
    }
    if reserved > paid || successful != refunded {
        return Err(conflict());
    }
    let transaction_id: String = pay
        .try_get::<Option<String>, _>("wechat_transaction_id")?
        .ok_or_else(conflict)?;
    if transaction_id.is_empty()
        || transaction_id.len() > 32
        || !transaction_id.bytes().all(|v| v.is_ascii_alphanumeric())
    {
        return Err(conflict());
    }
    let result = ExecutionDetail {
        refund_no: req.refund_no,
        status: if status == "pending" {
            "processing".into()
        } else {
            status
        },
        transaction_id,
        refund_cents: i32::try_from(row.try_get::<i64, _>("refund_cents")?)
            .map_err(|_| conflict())?,
        total_cents: i32::try_from(total).map_err(|_| conflict())?,
        wechat_refund_id: row.try_get("wechat_refund_id")?,
    };
    if claimed.is_none() {
        sqlx::query("INSERT INTO refund_execution (refund_record_id) VALUES (?)")
            .bind(rid)
            .execute(&mut *tx)
            .await?;
        sqlx::query(
            "UPDATE refund_record SET status='processing',claimed_at=UTC_TIMESTAMP(3) WHERE id=?",
        )
        .bind(rid)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        result,
        common_error::current_request_id(),
    )))
}
