//! Allocate original recharge payments and reserve wallet funds in the same transaction.
use common_error::{AppError, AppResult};
use serde_json::{json, Value};
use sqlx::Row;
fn conflict() -> AppError {
    AppError::Conflict("钱包或充值退款记录不一致，请联系客服".into())
}
#[derive(serde::Deserialize)]
pub struct ListQuery{pub page:Option<u32>,pub page_size:Option<u32>}
pub async fn list(axum::extract::State(st):axum::extract::State<crate::AppState>,claims:common_auth::UserClaims,axum::extract::Query(query):axum::extract::Query<ListQuery>)->AppResult<axum::Json<common_error::ApiEnvelope<Value>>>{
    let page=query.page.unwrap_or(1);let size=query.page_size.unwrap_or(20);
    if page==0 || page>100000 || size==0 || size>50{return Err(AppError::BadRequest("分页参数无效".into()));}
    let data=list_for_user(st.db.pool(),claims.user_id,page,size).await?;
    Ok(axum::Json(common_error::ApiEnvelope::ok(data,common_error::current_request_id())))
}
async fn list_for_user(pool:&sqlx::MySqlPool,uid:u64,page:u32,size:u32)->AppResult<Value>{
    let mut tx=pool.begin().await?;
    let total:i64=sqlx::query_scalar("SELECT COUNT(*) FROM wallet_refund_request WHERE user_id=?").bind(uid).fetch_one(&mut *tx).await?;
    let rows=sqlx::query("SELECT CAST(request_id AS CHAR CHARACTER SET utf8mb4) AS request_id,amount_cents,reason,response_json,created_at FROM wallet_refund_request WHERE user_id=? ORDER BY created_at DESC,request_id DESC LIMIT ? OFFSET ?").bind(uid).bind(size).bind(u64::from(page-1)*u64::from(size)).fetch_all(&mut *tx).await?;
    let mut items=Vec::new();
    for row in rows {
        let id:String=row.try_get("request_id")?;
        let saved:Option<Value>=row.try_get("response_json")?;
        let parts=sqlx::query("SELECT r.refund_no,r.refund_cents,r.status,r.failure_reason,r.completed_at FROM wallet_refund_part p JOIN refund_record r ON r.id=p.refund_record_id WHERE p.request_id=? AND r.user_id=? AND r.deleted_at IS NULL ORDER BY r.id").bind(&id).bind(uid).fetch_all(&mut *tx).await?;
        let mut orders=Vec::new();let mut refunded=0i64;let mut statuses=Vec::new();
        for part in parts {
            let status:String=part.try_get("status")?;let amount:i64=part.try_get("refund_cents")?;
            if status=="success"{refunded=refunded.checked_add(amount).ok_or_else(conflict)?;}
            statuses.push(status.clone());
            orders.push(json!({"refund_no":part.try_get::<String,_>("refund_no")?,"refund_cents":amount,"status":status,"failure_reason":part.try_get::<Option<String>,_>("failure_reason")?,"completed_at":part.try_get::<Option<chrono::NaiveDateTime>,_>("completed_at")?.map(|v|v.and_utc().to_rfc3339())}));
        }
        let amount:i64=row.try_get("amount_cents")?;
        let status=if saved.as_ref().and_then(|v|v.get("status")).and_then(Value::as_str)==Some("manual_review"){"manual_review"}else if statuses.is_empty() || statuses.iter().any(|s|s=="failed"){"needs_review"}else if refunded==amount && statuses.iter().all(|s|s=="success"){"success"}else if statuses.iter().any(|s|s=="processing" || s=="success"){"processing"}else{"pending"};
        items.push(json!({"request_id":id,"amount_cents":amount,"refunded_cents":refunded,"status":status,"reason":row.try_get::<Option<String>,_>("reason")?,"created_at":row.try_get::<chrono::NaiveDateTime,_>("created_at")?.and_utc().to_rfc3339(),"refund_orders":orders}));
    }
    tx.commit().await?;Ok(json!({"user_id":uid.to_string(),"items":items,"total":total,"page":page,"page_size":size}))
}
pub async fn apply(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    uid: u64,
    req: &crate::wallet::WalletRefundReq,
) -> AppResult<Value> {
    let request = uuid::Uuid::parse_str(&req.request_id)
        .map_err(|_| AppError::BadRequest("退款请求标识无效".into()))?
        .to_string();
    if !(1..=100_000_000).contains(&req.amount_cents)
        || req
            .reason
            .as_ref()
            .is_some_and(|v| v.chars().count() > 255 || v.chars().any(char::is_control))
    {
        return Err(AppError::BadRequest("退款金额或原因无效".into()));
    }
    sqlx::query("INSERT IGNORE INTO wallet_refund_request (request_id,user_id,amount_cents,reason) VALUES (?,?,?,?)").bind(&request).bind(uid).bind(req.amount_cents).bind(&req.reason).execute(&mut **tx).await?;
    let prior=sqlx::query("SELECT user_id,amount_cents,reason,response_json FROM wallet_refund_request WHERE request_id=? FOR UPDATE").bind(&request).fetch_one(&mut **tx).await?;
    if prior.try_get::<u64, _>("user_id")? != uid
        || prior.try_get::<i64, _>("amount_cents")? != req.amount_cents
        || prior.try_get::<Option<String>, _>("reason")? != req.reason
    {
        return Err(conflict());
    }
    if let Some(response) = prior.try_get::<Option<Value>, _>("response_json")? {
        return Ok(response);
    }
    // Same payment-before-wallet order as the recharge callback and refund result.
    let payments=sqlx::query("SELECT id,order_no,biz_id,paid_cents,refunded_cents FROM payment_order WHERE user_id=? AND biz_type='wallet_recharge' AND pay_method='wechat' AND status IN ('paid','partial_refunded') AND paid_at>DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 365 DAY) AND wechat_transaction_id IS NOT NULL AND deleted_at IS NULL ORDER BY paid_at,id FOR UPDATE").bind(uid).fetch_all(&mut **tx).await?;
    let wallets=sqlx::query("SELECT id,balance_cents,frozen_cents,status FROM wallet_account WHERE user_id=? AND deleted_at IS NULL FOR UPDATE").bind(uid).fetch_all(&mut **tx).await?;
    if wallets.len() != 1 {
        return Err(conflict());
    }
    let wallet = &wallets[0];
    let wid: u64 = wallet.try_get("id")?;
    let balance: i64 = wallet.try_get("balance_cents")?;
    if wallet.try_get::<String, _>("status")? != "active" {
        return Err(AppError::Conflict("钱包已冻结，请联系客服审核".into()));
    }
    if balance < req.amount_cents {
        return Err(AppError::InsufficientBalance);
    }
    let recent:i64=sqlx::query_scalar("SELECT COUNT(*) FROM wallet_refund_request WHERE user_id=? AND created_at>DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 5 MINUTE)").bind(uid).fetch_one(&mut **tx).await?;
    let response = if recent >= 3 {
        sqlx::query("UPDATE wallet_account SET status='frozen',version=version+1 WHERE id=?")
            .bind(wid)
            .execute(&mut **tx)
            .await?;
        sqlx::query("INSERT INTO risk_freeze_log (user_id,trigger_rule,frozen_action,reason,window_minutes,threshold_value) VALUES (?,'wallet_refund_frequency','wallet_refund','五分钟内第三次钱包退款申请',5,3)").bind(uid).execute(&mut **tx).await?;
        json!({"request_id":request,"status":"manual_review","refund_cents":req.amount_cents,"refund_orders":[],"message":"退款频次较高，钱包已冻结，等待人工审核"})
    } else {
        let mut remaining = req.amount_cents;
        let mut allocations = Vec::new();
        for pay in payments {
            let pid: u64 = pay.try_get("id")?;
            let paid: i64 = pay.try_get("paid_cents")?;
            let refunded: i64 = pay.try_get("refunded_cents")?;
            if refunded < 0 || refunded > paid {
                return Err(conflict());
            }
            let refunds:Vec<(i64,String)>=sqlx::query_as("SELECT refund_cents,status FROM refund_record WHERE payment_order_id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
            let mut reserved = refunded;
            let mut successful = 0i64;
            for (amount, status) in &refunds {
                if *amount <= 0 {
                    return Err(conflict());
                }
                if status == "success" {
                    successful = successful.checked_add(*amount).ok_or_else(conflict)?;
                } else {
                    reserved = reserved.checked_add(*amount).ok_or_else(conflict)?;
                }
            }
            if successful != refunded || reserved > paid {
                return Err(conflict());
            }
            let amount = (paid - reserved).min(remaining);
            if amount == 0 || refunds.len() >= 50 {
                continue;
            }
            allocations.push((
                pid,
                pay.try_get::<String, _>("order_no")?,
                pay.try_get::<u64, _>("biz_id")?,
                amount,
            ));
            remaining -= amount;
            if remaining == 0 {
                break;
            }
        }
        if remaining != 0 {
            return Err(AppError::Conflict(
                "可原路退回的充值金额不足或超过退款期限，请联系客服".into(),
            ));
        }
        let previous_frozen = wallet.try_get::<i64, _>("frozen_cents")?;
        if previous_frozen < 0 {
            return Err(conflict());
        }
        let frozen = previous_frozen
            .checked_add(req.amount_cents)
            .ok_or_else(conflict)?;
        sqlx::query(
            "UPDATE wallet_account SET balance_cents=?,frozen_cents=?,version=version+1 WHERE id=?",
        )
        .bind(balance - req.amount_cents)
        .bind(frozen)
        .bind(wid)
        .execute(&mut **tx)
        .await?;
        let txn = common_db::IdGen::new("WTX").next();
        sqlx::query("INSERT INTO wallet_txn (txn_no,user_id,wallet_account_id,direction,amount_cents,balance_after_cents,biz_type,biz_ref,note,created_month) VALUES (?,?,?,'out',?,?,'freeze',?,'钱包退款资金预留',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(&txn).bind(uid).bind(wid).bind(req.amount_cents).bind(balance-req.amount_cents).bind(&request).execute(&mut **tx).await?;
        let mut parts = Vec::new();
        for (pid, payment_no, biz, amount) in allocations {
            let no = common_db::IdGen::new("REF").next();
            let rid=sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES (?,?,?,'wallet_recharge',?,?,'钱包余额原路退款','pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(&no).bind(pid).bind(uid).bind(biz).bind(amount).execute(&mut **tx).await?.last_insert_id();
            sqlx::query("INSERT INTO wallet_refund_part (refund_record_id,request_id,wallet_account_id,amount_cents) VALUES (?,?,?,?)").bind(rid).bind(&request).bind(wid).bind(amount).execute(&mut **tx).await?;
            let event = common_redis::StreamEnvelope::new(
                "refund_required",
                "user",
                json!({"refund_no":no,"payment_order_id":pid,"user_id":uid,"refund_cents":amount}),
            );
            sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
                .bind(&event.event_id)
                .bind(common_redis::streams::REFUND_REQUIRED)
                .bind(serde_json::to_value(&event)?)
                .execute(&mut **tx)
                .await?;
            parts.push(json!({"refund_no":no,"payment_order_no":payment_no,"refund_cents":amount,"status":"pending"}));
        }
        json!({"request_id":request,"status":"accepted","txn_no":txn,"refund_cents":req.amount_cents,"refund_orders":parts})
    };
    sqlx::query("UPDATE wallet_refund_request SET response_json=? WHERE request_id=?")
        .bind(&response)
        .bind(request)
        .execute(&mut **tx)
        .await?;
    Ok(response)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires development user MySQL"]
    async fn splits_original_payments_reserves_once_and_settles_once() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let uid = sqlx::query("INSERT INTO user (openid) VALUES (?)")
            .bind(&tag)
            .execute(&mut *tx)
            .await
            .unwrap()
            .last_insert_id();
        let wid = sqlx::query("INSERT INTO wallet_account (user_id,balance_cents) VALUES (?,500)")
            .bind(uid)
            .execute(&mut *tx)
            .await
            .unwrap()
            .last_insert_id();
        let mut payments = Vec::new();
        for amount in [200, 300] {
            let id=sqlx::query("INSERT INTO payment_order (order_no,user_id,biz_type,biz_id,pay_method,total_cents,paid_cents,status,paid_at,wechat_transaction_id,created_month) VALUES (?,?,'wallet_recharge',1,'wechat',?,?,'paid',UTC_TIMESTAMP(3),?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(format!("PAY{amount}{tag}")).bind(uid).bind(amount).bind(amount).bind(format!("WX{amount}{tag}")).execute(&mut *tx).await.unwrap().last_insert_id();
            payments.push(id);
        }
        let req = crate::wallet::WalletRefundReq {
            request_id: uuid::Uuid::new_v4().to_string(),
            amount_cents: 250,
            reason: None,
        };
        let first = apply(&mut tx, uid, &req).await.unwrap();
        let replay = apply(&mut tx, uid, &req).await.unwrap();
        assert_eq!(first, replay);
        let parts = &first["refund_orders"].as_array().unwrap();
        assert_eq!(parts.len(), 2);
        assert_eq!(parts[0]["refund_cents"], 200);
        assert_eq!(parts[1]["refund_cents"], 50);
        let reserved: (i64, i64) =
            sqlx::query_as("SELECT balance_cents,frozen_cents FROM wallet_account WHERE id=?")
                .bind(wid)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(reserved, (250, 250));
        assert!(apply(&mut tx, uid + 1, &req).await.is_err());
        for (index, part) in parts.iter().enumerate() {
            let no = part["refund_no"].as_str().unwrap();
            sqlx::query("UPDATE refund_record SET status='processing' WHERE refund_no=?")
                .bind(no)
                .execute(&mut *tx)
                .await
                .unwrap();
            let result = crate::refund::ResultReq {
                refund_no: no.into(),
                success: true,
                wechat_refund_id: Some(format!("500{uid}{index}")),
                failure_reason: None,
            };
            crate::refund_result::apply(&mut tx, no, &result)
                .await
                .unwrap();
            crate::refund_result::apply(&mut tx, no, &result)
                .await
                .unwrap();
        }
        let settled: (i64, i64) =
            sqlx::query_as("SELECT balance_cents,frozen_cents FROM wallet_account WHERE id=?")
                .bind(wid)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(settled, (250, 0));
        let second = crate::wallet::WalletRefundReq {
            request_id: uuid::Uuid::new_v4().to_string(),
            amount_cents: 100,
            reason: None,
        };
        let second = apply(&mut tx, uid, &second).await.unwrap();
        assert_eq!(second["refund_orders"].as_array().unwrap().len(), 1);
        let third = crate::wallet::WalletRefundReq {
            request_id: uuid::Uuid::new_v4().to_string(),
            amount_cents: 1,
            reason: None,
        };
        let review = apply(&mut tx, uid, &third).await.unwrap();
        assert_eq!(review["status"], "manual_review");
        let frozen: (i64, i64, String) = sqlx::query_as(
            "SELECT balance_cents,frozen_cents,status FROM wallet_account WHERE id=?",
        )
        .bind(wid)
        .fetch_one(&mut *tx)
        .await
        .unwrap();
        assert_eq!(frozen, (150, 100, "frozen".into()));
        assert_eq!(apply(&mut tx, uid, &req).await.unwrap(), first);
        let invalid: i64 = sqlx::query_scalar(
            "SELECT COUNT(*) FROM refund_record WHERE user_id=? AND payment_order_id NOT IN (?,?)",
        )
        .bind(uid)
        .bind(payments[0])
        .bind(payments[1])
        .fetch_one(&mut *tx)
        .await
        .unwrap();
        assert_eq!(invalid, 0);
        tx.rollback().await.unwrap();
    }
}
