//! Release only the recorded wallet-refund risk cause; preserve other holds and money.
use common_error::{AppError,AppResult};
use serde_json::{json,Value};
use sqlx::Row;
#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Release {pub actor_id:u64,pub comment:String}
pub async fn apply(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,id:&str,req:&Release)->AppResult<Value>{
 let id=uuid::Uuid::parse_str(id).map_err(|_|AppError::BadRequest("申请编号无效".into()))?.to_string();
 if req.actor_id==0 || req.comment.trim().is_empty() || req.comment.chars().count()>255 || req.comment.chars().any(char::is_control){return Err(AppError::BadRequest("解冻依据无效".into()));}
 let uid:u64=sqlx::query_scalar("SELECT user_id FROM wallet_refund_request WHERE request_id=? FOR UPDATE").bind(&id).fetch_optional(&mut **tx).await?.ok_or_else(||AppError::NotFound("退款申请不存在".into()))?;
 let previous=sqlx::query("SELECT actor_id,comment,response_json FROM wallet_risk_release WHERE request_id=?").bind(&id).fetch_optional(&mut **tx).await?;
 if let Some(row)=previous{
  if row.try_get::<u64,_>("actor_id")?==req.actor_id && row.try_get::<String,_>("comment")?==req.comment.trim(){return Ok(row.try_get("response_json")?);}
  return Err(AppError::Conflict("该冻结原因已解除，请刷新记录".into()));
 }
 let reviewed:i64=sqlx::query_scalar("SELECT COUNT(*) FROM wallet_risk_review WHERE request_id=?").bind(&id).fetch_one(&mut **tx).await?;
 if reviewed!=1{return Err(AppError::Conflict("须先完成退款风控审核".into()));}
 let freeze_id:u64=sqlx::query_scalar("SELECT freeze_id FROM wallet_risk_freeze_link WHERE request_id=?").bind(&id).fetch_optional(&mut **tx).await?.ok_or_else(||AppError::Conflict("历史申请缺少明确冻结关联，请人工核查".into()))?;
 let user:Option<String>=sqlx::query_scalar("SELECT status FROM user WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(uid).fetch_optional(&mut **tx).await?;
 let wallets=sqlx::query("SELECT id,status FROM wallet_account WHERE user_id=? AND deleted_at IS NULL FOR UPDATE").bind(uid).fetch_all(&mut **tx).await?;
 if wallets.len()!=1{return Err(AppError::Conflict("钱包账户异常".into()));}
 let logs=sqlx::query("SELECT id,trigger_rule,frozen_action,status FROM risk_freeze_log WHERE user_id=? ORDER BY id FOR UPDATE").bind(uid).fetch_all(&mut **tx).await?;
 let target=logs.iter().find(|r|r.try_get::<u64,_>("id").ok()==Some(freeze_id)).ok_or_else(||AppError::Conflict("冻结记录不匹配".into()))?;
 if target.try_get::<String,_>("trigger_rule")?!="wallet_refund_frequency" || target.try_get::<String,_>("frozen_action")?!="wallet_refund" || target.try_get::<String,_>("status")?!="frozen" {return Err(AppError::Conflict("冻结原因或状态已变化".into()));}
 let other_frozen=logs.iter().any(|r|r.try_get::<u64,_>("id").ok()!=Some(freeze_id)&&r.try_get::<String,_>("status").ok().as_deref()==Some("frozen"));
 // Existing frozen user identity remains authoritative, even after this cause is resolved.
 let activated=!other_frozen && user.as_deref()==Some("active");
 sqlx::query("UPDATE risk_freeze_log SET status='unfrozen',unfreeze_at=UTC_TIMESTAMP(3) WHERE id=?").bind(freeze_id).execute(&mut **tx).await?;
 if activated{sqlx::query("UPDATE wallet_account SET status='active',version=version+1 WHERE id=? AND status='frozen'").bind(wallets[0].try_get::<u64,_>("id")?).execute(&mut **tx).await?;}
 let response=json!({"request_id":id,"freeze_id":freeze_id.to_string(),"released":true,"wallet_active":activated,"actor_id":req.actor_id.to_string(),"comment":req.comment.trim()});
 sqlx::query("INSERT INTO wallet_risk_release(request_id,actor_id,comment,response_json) VALUES(?,?,?,?)").bind(&id).bind(req.actor_id).bind(req.comment.trim()).bind(&response).execute(&mut **tx).await?;
 Ok(response)
}
pub async fn receive(axum::extract::State(st):axum::extract::State<crate::AppState>,axum::extract::Path(id):axum::extract::Path<String>,axum::Json(req):axum::Json<Release>)->AppResult<axum::Json<common_error::ApiEnvelope<Value>>>{
 let mut tx=st.db.pool().begin().await?;let value=apply(&mut tx,&id,&req).await?;tx.commit().await?;
 Ok(axum::Json(common_error::ApiEnvelope::ok(value,common_error::current_request_id())))
}
