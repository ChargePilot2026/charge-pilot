//! Manual charge refunds are created together with the finance applicant's first signature.
//!
//! ── repository 层豁免(refund 域)──
//! 人工退款与申请方首次签名同事务创建。`json!` 构造 `manual_refund_request.payload_json`,
//! 该列用于幂等比对「同一 request_id 不能换账号/订单/金额/原因」。
#![allow(clippy::disallowed_methods, clippy::disallowed_macros, clippy::disallowed_types)]

use common_error::{AppError,AppResult};
use serde_json::{json,Value};
use sqlx::Row;
#[derive(serde::Deserialize)]
pub struct Request {pub actor_id:u64,pub request:api_contracts::refunds::ManualRefundRequest}
pub async fn create(axum::extract::State(st):axum::extract::State<crate::AppState>,axum::extract::Path(cid):axum::extract::Path<u64>,axum::Json(req):axum::Json<Request>)->AppResult<axum::Json<common_error::ApiEnvelope<api_contracts::charge::ManualRefundCreated>>>{
 let result=apply(st.refund.pool(),cid,req.actor_id,&req.request).await?;
 Ok(axum::Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
async fn apply(pool:&sqlx::MySqlPool,cid:u64,actor:u64,req:&api_contracts::refunds::ManualRefundRequest)->AppResult<api_contracts::charge::ManualRefundCreated>{
 let uuid=uuid::Uuid::parse_str(&req.request_id).map_err(|_|AppError::BadRequest("申请标识必须为 UUID".into()))?;
 let request_id=uuid.to_string();let reason=req.reason.trim();
 if cid==0 || actor==0 || req.amount_cents<=0 || req.amount_cents>i32::MAX as i64 || reason.is_empty() || reason.chars().count()>255 || reason.chars().any(char::is_control){return Err(AppError::BadRequest("退款金额或原因无效".into()));}
 let payload=json!({"order_id":cid,"actor_id":actor,"amount_cents":req.amount_cents,"reason":reason});
 let mut tx=common_db::Tx::from_transaction(pool.begin().await?);
 sqlx::query("INSERT IGNORE INTO manual_refund_request(request_id,payload_json) VALUES (?,?)").bind(&request_id).bind(&payload).execute(tx.executor()).await?;
 let prior=sqlx::query("SELECT payload_json,refund_no FROM manual_refund_request WHERE request_id=? FOR UPDATE").bind(&request_id).fetch_one(tx.executor()).await?;
 if prior.try_get::<Value,_>("payload_json")?!=payload{return Err(AppError::Conflict("同一申请标识不能更换账号、订单、金额或原因".into()));}
 if let Some(no)=prior.try_get::<Option<String>,_>("refund_no")? {
  tx.commit().await?;return Ok(api_contracts::charge::ManualRefundCreated { request_id: request_id.clone(), refund_no: no.clone(), created: true });
 }
 let orders:Vec<(u64,Option<u64>)>=sqlx::query_as("SELECT user_id,payment_order_id FROM charge_order WHERE id=? AND deleted_at IS NULL").bind(cid).fetch_all(tx.executor()).await?;
 if orders.len()!=1{return Err(AppError::NotFound("订单不存在或重复".into()));}
 let(uid,pid)=orders[0];let pid=pid.ok_or_else(||AppError::Conflict("订单尚未支付".into()))?;
 let payments:Vec<u64>=sqlx::query_scalar("SELECT id FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(tx.executor()).await?;
 if payments.len()!=1{return Err(AppError::Conflict("支付记录不存在或重复".into()));}
 let no=format!("MAN{}",uuid.simple());
 sqlx::query("INSERT INTO refund_record(refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES (?,?,?,'charge',?,?,?,'pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(&no).bind(pid).bind(uid).bind(cid).bind(req.amount_cents).bind(reason).execute(tx.executor()).await?;
 // Reuse all terminal-order, ownership and remaining-refund checks, in this same transaction.
 crate::capability::refund::refund_review::approve(&mut tx,&no,actor,reason).await?;
 sqlx::query("UPDATE manual_refund_request SET refund_no=? WHERE request_id=?").bind(&no).bind(&request_id).execute(tx.executor()).await?;
 tx.commit().await?;
 Ok(api_contracts::charge::ManualRefundCreated { request_id: request_id.clone(), refund_no: no.clone(), created: true })
}
