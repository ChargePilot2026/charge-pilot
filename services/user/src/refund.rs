//! 退款编排(技术规格 § 5.4 + § 7.6 退款 SOP)
//!
//! - claim: admin 消费 refund_required_stream → 幂等领取
//! - result: admin 调微信退款 → 写结果回 user
//! - detail: 退款详情查询

use crate::AppState;
use axum::{
    extract::{Path, State},
    Json,
};
use common_db::IdGen;
use common_error::{AppError, AppResult};

use serde::Deserialize;
use serde_json::json;

pub async fn list(State(st):State<AppState>,axum::extract::Query(q):axum::extract::Query<api_contracts::refunds::RefundQuery>)->AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>>{
    use sqlx::Row;
    if !q.valid(){return Err(AppError::BadRequest("退款筛选参数无效".into()));}
    let mut tx=st.db.pool().begin().await?;
    let total:i64=sqlx::query_scalar("SELECT COUNT(*) FROM refund_record WHERE deleted_at IS NULL AND (? IS NULL OR status=?) AND (? IS NULL OR refund_no=?)")
        .bind(&q.status).bind(&q.status).bind(&q.refund_no).bind(&q.refund_no).fetch_one(&mut *tx).await?;
    let page=q.page.unwrap_or(1);let size=q.page_size.unwrap_or(20);
    let rows=sqlx::query("SELECT id,refund_no,user_id,payment_order_id,biz_type,refund_cents,status,reason,failure_reason,created_at,completed_at FROM refund_record WHERE deleted_at IS NULL AND (? IS NULL OR status=?) AND (? IS NULL OR refund_no=?) ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?")
        .bind(&q.status).bind(&q.status).bind(&q.refund_no).bind(&q.refund_no).bind(size).bind(u64::from(page-1)*u64::from(size)).fetch_all(&mut *tx).await?;
    let mut items=Vec::new();
    for row in rows {
        items.push(json!({"id":row.try_get::<u64,_>("id")?.to_string(),"refund_no":row.try_get::<String,_>("refund_no")?,"user_id":row.try_get::<u64,_>("user_id")?.to_string(),"payment_order_id":row.try_get::<u64,_>("payment_order_id")?.to_string(),"biz_type":row.try_get::<String,_>("biz_type")?,"refund_cents":row.try_get::<i64,_>("refund_cents")?,"status":row.try_get::<String,_>("status")?,"reason":row.try_get::<Option<String>,_>("reason")?,"failure_reason":row.try_get::<Option<String>,_>("failure_reason")?,"created_at":row.try_get::<chrono::NaiveDateTime,_>("created_at")?.and_utc().to_rfc3339(),"completed_at":row.try_get::<Option<chrono::NaiveDateTime>,_>("completed_at")?.map(|t|t.and_utc().to_rfc3339())}));
        let review=sqlx::query("SELECT first_signer,second_signer,first_comment,second_comment,approved_at FROM refund_review WHERE refund_record_id=?").bind(row.try_get::<u64,_>("id")?).fetch_optional(&mut *tx).await?;
        if let Some(review)=review {
            let second:Option<u64>=review.try_get("second_signer")?;
            let item=items.last_mut().expect("just pushed");
            item["review"]=json!({"status":if second.is_some(){"approved"}else{"awaiting_second"},"first_signer":review.try_get::<u64,_>("first_signer")?.to_string(),"second_signer":second.map(|v|v.to_string()),"first_comment":review.try_get::<String,_>("first_comment")?,"second_comment":review.try_get::<Option<String>,_>("second_comment")?,"approved_at":review.try_get::<Option<chrono::NaiveDateTime>,_>("approved_at")?.map(|v|v.and_utc().to_rfc3339())});
        }
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items":items,"total":total,"page":page,"page_size":size}),common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct ClaimReq {
    pub event_id: String,
    pub refund_no: String,
    pub admin_user_id: u64,
}

pub async fn claim(
    State(st): State<AppState>,
    Json(req): Json<ClaimReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    if req.admin_user_id == 0 || uuid::Uuid::parse_str(&req.event_id).is_err() {
        return Err(AppError::BadRequest("退款领取身份或事件标识无效".into()));
    }
    let mut tx = st.db.pool().begin().await?;
    let rows:Vec<(u64,u64,i64,String,u64,Option<u64>)>=sqlx::query_as("SELECT id,user_id,refund_cents,status,payment_order_id,claimed_by FROM refund_record WHERE refund_no=? AND deleted_at IS NULL FOR UPDATE")
        .bind(&req.refund_no).fetch_all(&mut *tx).await?;
    if rows.len() != 1 {
        return Err(AppError::NotFound("退款不存在或重复".into()));
    }
    let (id, user_id, cents, mut status, pay_id, owner) = rows[0].clone();
    let unsigned:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM refund_review WHERE refund_record_id=? AND second_signer IS NULL)").bind(id).fetch_one(&mut *tx).await?;
    if unsigned{return Err(AppError::Conflict("退款尚未完成双签审核".into()));}
    if status == "pending" && owner.is_none() {
        sqlx::query("UPDATE refund_record SET claimed_by=?,claimed_at=UTC_TIMESTAMP(3),status='processing' WHERE id=?").bind(req.admin_user_id).bind(id).execute(&mut *tx).await?;
        status = "processing".into();
    } else if owner != Some(req.admin_user_id)
        || !["processing", "success", "failed"].contains(&status.as_str())
    {
        return Err(AppError::Conflict(
            "退款已由其他处理者领取或状态不允许领取".into(),
        ));
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        json!({
            "refund_no": req.refund_no,
            "user_id": user_id,
            "payment_order_id": pay_id,
            "refund_cents": cents,
            "status": status,
        }),
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct ResultReq {
    pub refund_no: String,
    pub success: bool,
    pub wechat_refund_id: Option<String>,
    pub failure_reason: Option<String>,
}

pub async fn result(
    State(st): State<AppState>,
    Path(refund_id): Path<String>,
    Json(req): Json<ResultReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    crate::refund_result::apply(&mut tx, &refund_id, &req).await?;
    tx.commit().await?;

    Ok(Json(common_error::ApiEnvelope::ok(
        json!({"ok": true}),
        common_error::current_request_id(),
    )))
}

pub async fn detail(
    State(st): State<AppState>,
    Path(refund_id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let r: Option<(u64, u64, i64, String, String, Option<String>)> = sqlx::query_as(
        "SELECT id, user_id, refund_cents, status, biz_type, wechat_refund_id
         FROM refund_record WHERE refund_no=? AND deleted_at IS NULL LIMIT 1",
    )
    .bind(&refund_id)
    .fetch_optional(st.db.pool())
    .await?;
    let r = r.ok_or_else(|| AppError::NotFound("refund".into()))?;
    let review:Option<(u64,Option<u64>)>=sqlx::query_as("SELECT first_signer,second_signer FROM refund_review WHERE refund_record_id=?").bind(r.0).fetch_optional(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        json!({
            "id": r.0,
            "user_id": r.1,
            "refund_cents": r.2,
            "status": r.3,
            "biz_type": r.4,
            "wechat_refund_id": r.5,
            "first_signer": review.map(|v|v.0.to_string()),
            "second_signer": review.and_then(|v|v.1).map(|v|v.to_string()),
        }),
        common_error::current_request_id(),
    )))
}

/// 给 billing 用的辅助:写一条 pending refund_record
pub async fn create_refund_record(
    st: &AppState,
    pay_order_id: u64,
    user_id: u64,
    refund_cents: i64,
    biz_type: &str,
    biz_id: u64,
    reason: Option<&str>,
) -> AppResult<String> {
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let refund_no = IdGen::new("RFD").next();
    sqlx::query(
        "INSERT INTO refund_record (refund_no, payment_order_id, user_id, biz_type, biz_id, refund_cents, reason, status, created_month)
         VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)"
    )
    .bind(&refund_no)
    .bind(pay_order_id)
    .bind(user_id)
    .bind(biz_type)
    .bind(biz_id)
    .bind(refund_cents)
    .bind(reason)
    .bind(&now_month)
    .execute(st.db.pool())
    .await?;
    Ok(refund_no)
}
