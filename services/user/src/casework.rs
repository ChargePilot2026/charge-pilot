//! Internal customer service and inspection queues backed by user_db.

use crate::AppState;
use axum::{extract::{Path, Query, State}, Json};
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};
use sqlx::{MySql, QueryBuilder, Row};

#[derive(Debug, Deserialize)]
pub struct QueueQuery {
    pub status: Option<String>,
    pub page: Option<u32>,
    pub page_size: Option<u32>,
}

fn paging(q: &QueueQuery) -> AppResult<(u32, u32, u64)> {
    let page = q.page.unwrap_or(1);
    let size = q.page_size.unwrap_or(20);
    if page == 0 || page > 100_000 || !(1..=100).contains(&size) {
        return Err(AppError::BadRequest("page 必须为 1–100000，page_size 必须为 1–100".into()));
    }
    Ok((page, size, u64::from(page - 1) * u64::from(size)))
}

pub async fn feedback_list(
    State(st): State<AppState>,
    Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if q.status.as_deref().is_some_and(|s| !["pending", "processed", "closed"].contains(&s)) {
        return Err(AppError::BadRequest("feedback status 无效".into()));
    }
    let (page, page_size, offset) = paging(&q)?;
    let mut count = QueryBuilder::<MySql>::new("SELECT COUNT(*) FROM feedback WHERE deleted_at IS NULL");
    if let Some(status) = q.status.as_deref() { count.push(" AND status = ").push_bind(status); }
    let total: i64 = count.build_query_scalar().fetch_one(st.db.pool()).await?;

    let mut query = QueryBuilder::<MySql>::new(
        "SELECT id,user_id,order_id,device_id,rating,category,content,images_json,status,
                replied_by,replied_at,reply_content,created_at
         FROM feedback WHERE deleted_at IS NULL",
    );
    if let Some(status) = q.status.as_deref() { query.push(" AND status = ").push_bind(status); }
    query.push(" ORDER BY created_at DESC,id DESC LIMIT ").push_bind(page_size)
        .push(" OFFSET ").push_bind(offset);
    let rows = query.build().fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> {
        Ok(json!({
            "id": sqlx::Row::try_get::<u64, _>(r, "id")?.to_string(),
            "user_id": sqlx::Row::try_get::<u64, _>(r, "user_id")?.to_string(),
            "order_id": sqlx::Row::try_get::<Option<u64>, _>(r, "order_id")?.map(|v| v.to_string()),
            "device_id": sqlx::Row::try_get::<Option<String>, _>(r, "device_id")?,
            "rating": sqlx::Row::try_get::<Option<u8>, _>(r, "rating")?,
            "category": sqlx::Row::try_get::<String, _>(r, "category")?,
            "content": sqlx::Row::try_get::<Option<String>, _>(r, "content")?,
            "images": sqlx::Row::try_get::<Option<Value>, _>(r, "images_json")?.unwrap_or_else(|| json!([])),
            "status": sqlx::Row::try_get::<String, _>(r, "status")?,
            "replied_by": sqlx::Row::try_get::<Option<u64>, _>(r, "replied_by")?.map(|v| v.to_string()),
            "replied_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "replied_at")?.map(|v| v.to_rfc3339()),
            "reply_content": sqlx::Row::try_get::<Option<String>, _>(r, "reply_content")?,
            "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
        }))
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items":items,"total":total,"page":page,"page_size":page_size}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FeedbackAction {
    pub actor_id: u64,
    pub action: String,
    pub reply_content: Option<String>,
}

pub async fn feedback_reply(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<FeedbackAction>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let feedback_id = id.parse::<u64>().map_err(|_| AppError::BadRequest("反馈编号无效".into()))?;
    if req.actor_id == 0 || !["reply", "close"].contains(&req.action.as_str()) {
        return Err(AppError::BadRequest("反馈处理动作无效".into()));
    }
    let reply = req.reply_content.as_deref().map(str::trim).filter(|s| !s.is_empty());
    if req.action == "reply" && reply.is_none() {
        return Err(AppError::BadRequest("回复内容不能为空".into()));
    }
    if reply.is_some_and(|s| s.chars().count() > 2000 || s.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("回复内容最多 2000 字且不能包含控制字符".into()));
    }
    let mut tx = st.db.pool().begin().await?;
    let row = sqlx::query("SELECT status,replied_by,reply_content FROM feedback WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(feedback_id).fetch_optional(&mut *tx).await?.ok_or_else(|| AppError::NotFound("反馈不存在".into()))?;
    let status: String = row.try_get("status")?;
    let old_actor: Option<u64> = row.try_get("replied_by")?;
    let old_reply: Option<String> = row.try_get("reply_content")?;
    if req.action == "reply" && status == "processed" && old_actor == Some(req.actor_id) && old_reply.as_deref() == reply {
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(json!({"status":"processed","already_processed":true}), common_error::current_request_id())));
    }
    if req.action == "close" && status == "closed" {
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(json!({"status":"closed","already_processed":true}), common_error::current_request_id())));
    }
    let next_status = if req.action == "reply" { "processed" } else { "closed" };
    if req.action == "reply" && status != "pending" {
        return Err(AppError::Conflict("只有待处理反馈可以回复".into()));
    }
    if req.action == "close" && !["pending", "processed"].contains(&status.as_str()) {
        return Err(AppError::Conflict("该反馈已关闭".into()));
    }
    if req.action == "reply" {
        sqlx::query("UPDATE feedback SET status='processed',replied_by=?,replied_at=UTC_TIMESTAMP(3),reply_content=? WHERE id=?")
            .bind(req.actor_id).bind(reply).bind(feedback_id).execute(&mut *tx).await?;
    } else {
        sqlx::query("UPDATE feedback SET status='closed' WHERE id=?").bind(feedback_id).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"status":next_status,"already_processed":false}), common_error::current_request_id())))
}

pub async fn fault_list(
    State(st): State<AppState>,
    Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if q.status.as_deref().is_some_and(|s| !["open", "dispatched", "fixed", "closed"].contains(&s)) {
        return Err(AppError::BadRequest("fault status 无效".into()));
    }
    let (page, page_size, offset) = paging(&q)?;
    let mut count = QueryBuilder::<MySql>::new("SELECT COUNT(*) FROM device_fault_report WHERE deleted_at IS NULL");
    if let Some(status) = q.status.as_deref() { count.push(" AND status = ").push_bind(status); }
    let total: i64 = count.build_query_scalar().fetch_one(st.db.pool()).await?;
    let mut query = QueryBuilder::<MySql>::new(
        "SELECT id,device_id,user_id,report_source,fault_type,description,images_json,status,assigned_to,resolved_at,created_at,updated_at
         FROM device_fault_report WHERE deleted_at IS NULL",
    );
    if let Some(status) = q.status.as_deref() { query.push(" AND status = ").push_bind(status); }
    query.push(" ORDER BY created_at DESC,id DESC LIMIT ").push_bind(page_size).push(" OFFSET ").push_bind(offset);
    let rows = query.build().fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r,"id")?.to_string(),
        "device_id": sqlx::Row::try_get::<String, _>(r,"device_id")?,
        "user_id": sqlx::Row::try_get::<Option<u64>, _>(r,"user_id")?.map(|v|v.to_string()),
        "report_source": sqlx::Row::try_get::<String, _>(r,"report_source")?,
        "fault_type": sqlx::Row::try_get::<String, _>(r,"fault_type")?,
        "description": sqlx::Row::try_get::<Option<String>, _>(r,"description")?,
        "images": sqlx::Row::try_get::<Option<Value>, _>(r,"images_json")?.unwrap_or_else(||json!([])),
        "status": sqlx::Row::try_get::<String, _>(r,"status")?,
        "assigned_to": sqlx::Row::try_get::<Option<u64>, _>(r,"assigned_to")?.map(|v|v.to_string()),
        "resolved_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r,"resolved_at")?.map(|v|v.to_rfc3339()),
        "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r,"created_at")?.to_rfc3339(),
        "updated_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r,"updated_at")?.to_rfc3339(),
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items":items,"total":total,"page":page,"page_size":page_size}), common_error::current_request_id())))
}

fn validate_fault_note(note: Option<&str>, required: bool) -> AppResult<Option<String>> {
    let note = note.map(str::trim).filter(|value| !value.is_empty());
    if required && note.is_none() {
        return Err(AppError::BadRequest("修复备注不能为空".into()));
    }
    if note.is_some_and(|value| value.chars().count() > 2000 || value.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("处理备注最多 2000 字且不能包含控制字符".into()));
    }
    Ok(note.map(str::to_owned))
}

async fn fault_history_rows(
    st: &AppState,
    report_id: u64,
    q: &QueueQuery,
    user_id: Option<u64>,
) -> AppResult<Value> {
    let (page, page_size, offset) = paging(q)?;
    let report_exists: bool = if let Some(user_id) = user_id {
        sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM device_fault_report WHERE id=? AND user_id=? AND deleted_at IS NULL)")
            .bind(report_id).bind(user_id).fetch_one(st.db.pool()).await?
    } else {
        sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM device_fault_report WHERE id=? AND deleted_at IS NULL)")
            .bind(report_id).fetch_one(st.db.pool()).await?
    };
    if !report_exists { return Err(AppError::NotFound("报修不存在".into())); }

    let (total, rows) = if user_id.is_some() {
        let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM device_fault_report_event WHERE report_id=? AND user_visible=1")
            .bind(report_id).fetch_one(st.db.pool()).await?;
        let rows = sqlx::query(
            "SELECT id,event_type,from_status,to_status,note,created_at
             FROM device_fault_report_event WHERE report_id=? AND user_visible=1
             ORDER BY created_at,id LIMIT ? OFFSET ?",
        ).bind(report_id).bind(page_size).bind(offset).fetch_all(st.db.pool()).await?;
        (total, rows)
    } else {
        let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM device_fault_report_event WHERE report_id=?")
            .bind(report_id).fetch_one(st.db.pool()).await?;
        let rows = sqlx::query(
            "SELECT id,event_type,actor_id,from_status,to_status,assigned_to,note,user_visible,created_at
             FROM device_fault_report_event WHERE report_id=?
             ORDER BY created_at,id LIMIT ? OFFSET ?",
        ).bind(report_id).bind(page_size).bind(offset).fetch_all(st.db.pool()).await?;
        (total, rows)
    };
    let items = rows.iter().map(|row| -> AppResult<Value> {
        let mut item = json!({
            "event_id": row.try_get::<u64, _>("id")?.to_string(),
            "event_type": row.try_get::<String, _>("event_type")?,
            "from_status": row.try_get::<Option<String>, _>("from_status")?,
            "to_status": row.try_get::<Option<String>, _>("to_status")?,
            "note": row.try_get::<Option<String>, _>("note")?,
            "created_at": row.try_get::<chrono::DateTime<chrono::Utc>, _>("created_at")?.to_rfc3339(),
        });
        if user_id.is_none() {
            item["actor_id"] = json!(row.try_get::<Option<u64>, _>("actor_id")?.map(|v| v.to_string()));
            item["assigned_to"] = json!(row.try_get::<Option<u64>, _>("assigned_to")?.map(|v| v.to_string()));
            item["user_visible"] = json!(row.try_get::<bool, _>("user_visible")?);
        }
        Ok(item)
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(json!({"items":items,"total":total,"page":page,"page_size":page_size}))
}

pub async fn fault_history(
    State(st): State<AppState>, Path(id): Path<String>, Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let report_id = id.parse::<u64>().map_err(|_| AppError::BadRequest("报修编号无效".into()))?;
    let result = fault_history_rows(&st, report_id, &q, None).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultDispatch {
    pub actor_id: u64,
    pub assigned_to: u64,
    #[serde(default)]
    pub note: Option<String>,
}

pub async fn fault_dispatch(
    State(st): State<AppState>, Path(id): Path<String>, Json(req): Json<FaultDispatch>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let report_id = id.parse::<u64>().map_err(|_| AppError::BadRequest("报修编号无效".into()))?;
    if req.actor_id == 0 || req.assigned_to == 0 { return Err(AppError::BadRequest("派单账号无效".into())); }
    let note = validate_fault_note(req.note.as_deref(), false)?;
    let mut tx = st.db.pool().begin().await?;
    let row = sqlx::query("SELECT status,assigned_to FROM device_fault_report WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(report_id).fetch_optional(&mut *tx).await?.ok_or_else(||AppError::NotFound("报修不存在".into()))?;
    let status: String = row.try_get("status")?;
    let assigned_to: Option<u64> = row.try_get("assigned_to")?;
    if status == "dispatched" && assigned_to == Some(req.assigned_to) {
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(json!({"status":"dispatched","assigned_to":req.assigned_to.to_string(),"already_processed":true}), common_error::current_request_id())));
    }
    if !["open", "dispatched"].contains(&status.as_str()) { return Err(AppError::Conflict("只有待派单或处理中报修可以派单".into())); }
    sqlx::query("UPDATE device_fault_report SET status='dispatched',assigned_to=?,resolved_at=NULL WHERE id=?")
        .bind(req.assigned_to).bind(report_id).execute(&mut *tx).await?;
    sqlx::query(
        "INSERT INTO device_fault_report_event(report_id,actor_id,event_type,from_status,to_status,assigned_to,note,user_visible,created_at)
         VALUES(?,?,?,?,?,?,?,1,UTC_TIMESTAMP(3))",
    ).bind(report_id).bind(req.actor_id).bind(if status == "open" {"dispatched"} else {"reassigned"})
        .bind(&status).bind("dispatched").bind(req.assigned_to).bind(note)
        .execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"status":"dispatched","assigned_to":req.assigned_to.to_string(),"already_processed":false}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultResolve {
    pub actor_id: u64,
    pub status: String,
    #[serde(default)]
    pub note: Option<String>,
}

pub async fn fault_resolve(
    State(st): State<AppState>, Path(id): Path<String>, Json(req): Json<FaultResolve>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let report_id = id.parse::<u64>().map_err(|_| AppError::BadRequest("报修编号无效".into()))?;
    if req.actor_id == 0 || !["fixed", "closed"].contains(&req.status.as_str()) { return Err(AppError::BadRequest("处理状态无效".into())); }
    let note = validate_fault_note(req.note.as_deref(), req.status == "fixed")?;
    let mut tx = st.db.pool().begin().await?;
    let row = sqlx::query("SELECT status,assigned_to FROM device_fault_report WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(report_id).fetch_optional(&mut *tx).await?.ok_or_else(||AppError::NotFound("报修不存在".into()))?;
    let status: String = row.try_get("status")?;
    let assigned_to: Option<u64> = row.try_get("assigned_to")?;
    if status == req.status && assigned_to == Some(req.actor_id) {
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(json!({"status":req.status,"already_processed":true}), common_error::current_request_id())));
    }
    if assigned_to != Some(req.actor_id) { return Err(AppError::Forbidden("只有当前指派的巡检人员可以更新报修状态".into())); }
    let allowed = (req.status == "fixed" && status == "dispatched") || (req.status == "closed" && status == "fixed");
    if !allowed { return Err(AppError::Conflict("报修状态必须按处理中、已修复、已关闭顺序流转".into())); }
    if req.status == "fixed" {
        sqlx::query("UPDATE device_fault_report SET status='fixed',resolved_at=UTC_TIMESTAMP(3) WHERE id=?").bind(report_id).execute(&mut *tx).await?;
    } else {
        sqlx::query("UPDATE device_fault_report SET status='closed' WHERE id=?").bind(report_id).execute(&mut *tx).await?;
    }
    sqlx::query(
        "INSERT INTO device_fault_report_event(report_id,actor_id,event_type,from_status,to_status,assigned_to,note,user_visible,created_at)
         VALUES(?,?,?,?,?,?,?,1,UTC_TIMESTAMP(3))",
    ).bind(report_id).bind(req.actor_id).bind(&req.status).bind(&status).bind(&req.status)
        .bind(assigned_to).bind(note).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"status":req.status,"already_processed":false}), common_error::current_request_id())))
}
