//! Two separate authenticated finance approvals, bound to an immutable refund snapshot.
//! The admin service authorizes actor identity and role before calling this internal operation.
use common_error::{AppError, AppResult};
use serde_json::{json, Value};
use sqlx::Row;

fn conflict()->AppError {AppError::Conflict("退款状态或审核快照已变化，不能继续审核".into())}

#[derive(serde::Deserialize)]
pub struct Approval {pub actor_id:u64,pub comment:String}
pub async fn reject_receive(axum::extract::State(st):axum::extract::State<crate::AppState>,axum::extract::Path(no):axum::extract::Path<String>,axum::Json(req):axum::Json<Approval>)->AppResult<axum::Json<common_error::ApiEnvelope<Value>>>{
    let mut tx=st.db.pool().begin().await?;
    let result=reject(&mut tx,&no,req.actor_id,&req.comment).await?;
    tx.commit().await?;
    Ok(axum::Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
async fn reject(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,no:&str,actor:u64,reason:&str)->AppResult<Value>{
    let reason=reason.trim();
    if actor==0 || reason.is_empty() || reason.chars().count()>255 || reason.chars().any(char::is_control){return Err(AppError::BadRequest("拒绝身份或原因无效".into()));}
    let ids:Vec<(u64,u64)>=sqlx::query_as("SELECT id,payment_order_id FROM refund_record WHERE refund_no=? AND deleted_at IS NULL").bind(no).fetch_all(&mut **tx).await?;
    if ids.len()!=1{return Err(AppError::NotFound("退款不存在或重复".into()));}let(rid,pid)=ids[0];
    let pays:Vec<u64>=sqlx::query_scalar("SELECT id FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    if pays.len()!=1{return Err(conflict());}
    let row=sqlx::query("SELECT status,biz_type,biz_id,claimed_by FROM refund_record WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(rid).fetch_one(&mut **tx).await?;
    let prior:Option<(u64,String)>=sqlx::query_as("SELECT actor_id,reason FROM refund_rejection WHERE refund_record_id=?").bind(rid).fetch_optional(&mut **tx).await?;
    if let Some((who,original))=prior {
        if who==actor && original==reason && row.try_get::<String,_>("status")?=="rejected"{return Ok(json!({"refund_no":no,"review_status":"rejected"}));}
        return Err(conflict());
    }
    if row.try_get::<String,_>("status")?!="pending" || row.try_get::<String,_>("biz_type")?!="charge" || row.try_get::<Option<u64>,_>("claimed_by")?.is_some(){return Err(conflict());}
    let signed:Option<Option<u64>>=sqlx::query_scalar("SELECT second_signer FROM refund_review WHERE refund_record_id=? FOR UPDATE").bind(rid).fetch_optional(&mut **tx).await?;
    if signed!=Some(None){return Err(AppError::Conflict("仅待第二签的充电退款可以拒绝".into()));}
    let executing:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM refund_execution WHERE refund_record_id=?)").bind(rid).fetch_one(&mut **tx).await?;
    if executing{return Err(conflict());}
    sqlx::query("INSERT INTO refund_rejection(refund_record_id,actor_id,reason) VALUES (?,?,?)").bind(rid).bind(actor).bind(reason).execute(&mut **tx).await?;
    sqlx::query("UPDATE refund_record SET status='rejected',failure_reason=?,completed_at=UTC_TIMESTAMP(3) WHERE id=?").bind(reason).bind(rid).execute(&mut **tx).await?;
    crate::order_events::record(tx,row.try_get("biz_id")?,"refund_review_rejected",&format!("admin:{actor}"),reason).await?;
    Ok(json!({"refund_no":no,"review_status":"rejected"}))
}
pub async fn receive(axum::extract::State(st):axum::extract::State<crate::AppState>,axum::extract::Path(no):axum::extract::Path<String>,axum::Json(req):axum::Json<Approval>)->AppResult<axum::Json<common_error::ApiEnvelope<Value>>>{
    let mut tx=st.db.pool().begin().await?;
    let result=approve(&mut tx,&no,req.actor_id,&req.comment).await?;
    tx.commit().await?;
    Ok(axum::Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn approve(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,no:&str,actor:u64,comment:&str)->AppResult<Value> {
    let comment=comment.trim();
    if actor==0 || comment.is_empty() || comment.chars().count()>255 || comment.chars().any(char::is_control) {return Err(AppError::BadRequest("审核身份或意见无效".into()));}
    let ids:Vec<(u64,u64)>=sqlx::query_as("SELECT id,payment_order_id FROM refund_record WHERE refund_no=? AND deleted_at IS NULL").bind(no).fetch_all(&mut **tx).await?;
    if ids.len()!=1{return Err(AppError::NotFound("退款不存在或单号重复".into()));}
    let (rid,pid)=ids[0];
    let payments=sqlx::query("SELECT user_id,biz_id,biz_type,pay_method,total_cents,paid_cents,refunded_cents,status FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    if payments.len()!=1{return Err(conflict());}let pay=&payments[0];
    let refunds=sqlx::query("SELECT id,user_id,biz_id,biz_type,refund_cents,status,reason,claimed_by FROM refund_record WHERE payment_order_id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    let row=refunds.iter().find(|r|r.try_get::<u64,_>("id").ok()==Some(rid)).ok_or_else(conflict)?;
    if row.try_get::<String,_>("status")?=="rejected"{return Err(conflict());}
    let uid:u64=row.try_get("user_id")?;let cid:u64=row.try_get("biz_id")?;let amount:i64=row.try_get("refund_cents")?;
    if row.try_get::<String,_>("biz_type")?!="charge" || pay.try_get::<String,_>("biz_type")?!="charge" || pay.try_get::<String,_>("pay_method")?!="wechat" || pay.try_get::<u64,_>("user_id")?!=uid || pay.try_get::<u64,_>("biz_id")?!=cid {return Err(conflict());}
    let snapshot=json!({"refund_no":no,"payment_order_id":pid,"user_id":uid,"charge_order_id":cid,"refund_cents":amount,"reason":row.try_get::<Option<String>,_>("reason")?,"total_cents":pay.try_get::<i64,_>("total_cents")?,"paid_cents":pay.try_get::<i64,_>("paid_cents")?});
    let review=sqlx::query("SELECT snapshot_json,first_signer,first_comment,second_signer,second_comment FROM refund_review WHERE refund_record_id=? FOR UPDATE").bind(rid).fetch_optional(&mut **tx).await?;
    if let Some(review)=&review {
        if review.try_get::<Value,_>("snapshot_json")?!=snapshot {return Err(conflict());}
        let first:u64=review.try_get("first_signer")?;let second:Option<u64>=review.try_get("second_signer")?;
        if actor==first || second==Some(actor) {
            let original=if actor==first{review.try_get::<String,_>("first_comment")?}else{review.try_get::<Option<String>,_>("second_comment")?.ok_or_else(conflict)?};
            if comment!=original {return Err(conflict());}
            return Ok(json!({"refund_no":no,"review_status":if second.is_some(){"approved"}else{"awaiting_second"},"first_signer":first.to_string(),"second_signer":second.map(|v|v.to_string())}));
        }
        if second.is_some(){return Err(conflict());}
    }
    if row.try_get::<String,_>("status")?!="pending" || row.try_get::<Option<u64>,_>("claimed_by")?.is_some(){return Err(conflict());}
    let executed:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM refund_execution WHERE refund_record_id=?)").bind(rid).fetch_one(&mut **tx).await?;
    if executed{return Err(conflict());}
    let orders:Vec<(u64,Option<u64>,String)>=sqlx::query_as("SELECT user_id,payment_order_id,status FROM charge_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(cid).fetch_all(&mut **tx).await?;
    if orders.len()!=1 || orders[0].0!=uid || orders[0].1!=Some(pid) || !["completed","failed","cancelled"].contains(&orders[0].2.as_str()){return Err(conflict());}
    let paid:i64=pay.try_get("paid_cents")?;let refunded:i64=pay.try_get("refunded_cents")?;
    if amount<=0 || paid<=0 || paid!=pay.try_get::<i64,_>("total_cents")? || !["paid","partial_refunded"].contains(&pay.try_get::<String,_>("status")?.as_str()){return Err(conflict());}
    let mut success=0i64;let mut reserved=0i64;
    for r in &refunds {
        if r.try_get::<String,_>("status")?=="rejected"{continue;}
        let cents:i64=r.try_get("refund_cents")?;if cents<=0{return Err(conflict());}
        reserved=reserved.checked_add(cents).ok_or_else(conflict)?;
        if r.try_get::<String,_>("status")?=="success"{success=success.checked_add(cents).ok_or_else(conflict)?;}
    }
    if reserved>paid || success!=refunded{return Err(conflict());}
    let first=if let Some(review)=review {
        let first:u64=review.try_get("first_signer")?;
        sqlx::query("UPDATE refund_review SET second_signer=?,second_comment=?,approved_at=UTC_TIMESTAMP(3) WHERE refund_record_id=?").bind(actor).bind(comment).bind(rid).execute(&mut **tx).await?;
        let event=common_redis::StreamEnvelope::new("refund_required","user",json!({"refund_no":no,"payment_order_id":pid,"user_id":uid,"refund_cents":amount}));
        sqlx::query("INSERT INTO event_outbox(event_id,stream,envelope_json) VALUES (?,?,?)").bind(&event.event_id).bind(common_redis::streams::REFUND_REQUIRED).bind(serde_json::to_value(&event)?).execute(&mut **tx).await?;
        Some(first)
    } else {
        sqlx::query("INSERT INTO refund_review(refund_record_id,snapshot_json,first_signer,first_comment) VALUES (?,?,?,?)").bind(rid).bind(snapshot).bind(actor).bind(comment).execute(&mut **tx).await?;
        None
    };
    crate::order_events::record(tx,cid,if first.is_some(){"refund_review_approved"}else{"refund_review_first_signed"},&format!("admin:{actor}"),comment).await?;
    Ok(json!({"refund_no":no,"review_status":if first.is_some(){"approved"}else{"awaiting_second"},"first_signer":first.unwrap_or(actor).to_string(),"second_signer":first.map(|_|actor.to_string())}))
}
