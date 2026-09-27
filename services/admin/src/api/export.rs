//! 导出任务(简化为创建一个 worker 任务)

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::Value;

// 本文件四个端点全部是**未接入的桩**,永远走 `Err(ServiceUnavailable)`,
// 成功分支不存在。响应类型写 `()` 是如实标注"无成功响应体",
// 而不是留一个 `Value` 假装将来会有。
type Unavailable = Json<common_error::ApiEnvelope<()>>;

#[derive(Debug, Deserialize)]
pub struct ExportCreateReq {
    pub export_type: String,    // orders / settlements / invoices / alerts
    pub period_start: Option<chrono::DateTime<chrono::Utc>>,
    pub period_end: Option<chrono::DateTime<chrono::Utc>>,
    pub filters_json: Option<Value>,
}

pub async fn create(
    State(st): State<AppState>,
    actor: ActiveAdmin,
    Json(req): Json<ExportCreateReq>,
 ) -> AppResult<Unavailable> {
    crate::auth::require_permission(&st,&actor,"export.create").await?;
    let _ = (st, actor, req);
    Err(AppError::ServiceUnavailable(
        "导出执行器与文件存储尚未接入，未创建导出任务".into(),
    ))
}

pub async fn tasks(
    State(_st): State<AppState>,
    _actor: ActiveAdmin,
 ) -> AppResult<Unavailable> {
    unavailable()
}

pub async fn task(
    State(_st): State<AppState>,
    _actor: ActiveAdmin,
    Path(_task_id): Path<String>,
 ) -> AppResult<Unavailable> {
    unavailable()
}

pub async fn download(
    State(_st): State<AppState>,
    _actor: ActiveAdmin,
    Path(_task_id): Path<String>,
 ) -> AppResult<Unavailable> {
    unavailable()
}

fn unavailable() -> AppResult<Unavailable> {
    Err(AppError::ServiceUnavailable(
        "导出执行器、任务状态和文件存储尚未接入".into(),
    ))
}
