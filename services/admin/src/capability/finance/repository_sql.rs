//! finance 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 admin_db 的 `settled_record` / `refund_task` / `invoice_review` /
//! `audit_log` / `finance_reconcile_log`,以及退款与发票审核用的授权查询。

// `disallowed_types` 的豁免只服务于下方 `audit_log` 一节:`before_json` /
// `after_json` 是数据库 JSON 列,这里要做的是**原样透出**到列里,
// 载荷形状由各调用方的 DTO 决定,类型化反而会丢掉未知字段。
#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

use crate::AppState;
use common_error::{AppError, AppResult};
use sqlx::Row;

// ===== 权限与授权 =====

/// 退款列表的读权限。
pub async fn has_refund_read(st: &AppState, admin_user_id: u64) -> AppResult<bool> {
    let allowed: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND p.code='finance.refund.read'")
        .bind(admin_user_id).fetch_one(st.finance.pool()).await?;
    Ok(allowed > 0)
}

/// 退款重试权限。
pub async fn has_refund_retry(st: &AppState, admin_user_id: u64) -> AppResult<bool> {
    let n: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role_permission rp ON rp.role_id=a.role_id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND p.code='finance.refund.retry'")
        .bind(admin_user_id).fetch_one(st.finance.pool()).await?;
    Ok(n > 0)
}

/// 退款审核权限(需客户财务角色)。
pub async fn has_refund_review(st: &AppState, admin_user_id: u64) -> AppResult<bool> {
    let n: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code='order.refund.review'")
        .bind(admin_user_id).fetch_one(st.finance.pool()).await?;
    Ok(n > 0)
}

/// 事务内:审核人必须是具备 `order.refund.review` 权限的有效客户财务账号。
///
/// 供审核动作对首签人 / 审核人各锁一次(`FOR SHARE`),使权限在审核期间不被撤销。
pub async fn lock_refund_reviewer(
    tx: &mut common_db::Tx<'_>,
    id: u64,
) -> AppResult<()> {
    let rows: Vec<u64> = sqlx::query_scalar("SELECT a.id FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code='order.refund.review' FOR SHARE")
        .bind(id).fetch_all(tx.executor()).await?;
    if rows.is_empty() {
        return Err(AppError::Forbidden("审核人必须是具备 order.refund.review 权限的有效客户财务账号".into()));
    }
    Ok(())
}

/// 事务内:发起退款需客户财务角色及订单查看、退款申请、退款审核三项权限齐备。
pub async fn lock_refund_create_grants(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
) -> AppResult<()> {
    let grants: Vec<String> = sqlx::query_scalar("SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code IN ('order.read','order.refund.create','order.refund.review') FOR SHARE")
        .bind(admin_user_id).fetch_all(tx.executor()).await?;
    if grants.len() != 3 {
        return Err(AppError::Forbidden("发起退款需客户财务角色及订单查看、退款申请、退款审核权限".into()));
    }
    Ok(())
}

/// 事务内:退款重试权限。
pub async fn lock_refund_retry(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
) -> AppResult<()> {
    let allowed: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND p.code='finance.refund.retry'")
        .bind(admin_user_id).fetch_one(tx.executor()).await?;
    if allowed == 0 {
        return Err(AppError::Forbidden("缺少 finance.refund.retry 权限".into()));
    }
    Ok(())
}

/// 发票审核需要客户财务角色和 `invoice.review` 权限。
pub async fn authorize_invoice_review(st: &AppState, actor: &crate::capability::identity::ActiveAdmin) -> AppResult<()> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a
         JOIN role r ON r.id = a.role_id
         JOIN role_permission rp ON rp.role_id = r.id
         JOIN permission p ON p.id = rp.permission_id
         WHERE a.id = ? AND a.username = ? AND a.status = 'active' AND a.deleted_at IS NULL
           AND r.code = 'customer_finance' AND r.deleted_at IS NULL AND p.code = 'invoice.review')",
    )
    .bind(actor.admin_user_id)
    .bind(&actor.sub)
    .fetch_one(st.finance.pool())
    .await?;
    if !allowed { return Err(AppError::Forbidden("需要 customer_finance 角色和 invoice.review 权限".into())); }
    Ok(())
}

/// 事务内:钱包风控审核 / 解冻的授权。
///
/// `release` 为真时要求 `customer_finance` 角色,否则 `customer_cs` 也可审核。
pub async fn wallet_risk_authorize(
    tx: &mut common_db::Tx<'_>,
    actor: u64,
    release: bool,
) -> AppResult<()> {
    let permission = if release { "finance.wallet_risk.release" } else { "finance.wallet_risk.review" };
    let rows: Vec<u64> = sqlx::query_scalar("SELECT a.id FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code IN ('customer_finance','customer_cs') AND p.code=? AND (?=FALSE OR r.code='customer_finance') FOR SHARE")
        .bind(actor).bind(permission).bind(release).fetch_all(tx.executor()).await?;
    if rows.is_empty() {
        return Err(AppError::Forbidden(format!("角色或权限不足，需要 {permission}")));
    }
    Ok(())
}

/// 订单详情页判定"发起退款"入口所需的两项权限是否齐备。
pub async fn refund_create_grant_count(st: &AppState, admin_user_id: u64) -> AppResult<i64> {
    let grants: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code IN ('order.refund.create','order.refund.review')")
        .bind(admin_user_id).fetch_one(st.finance.pool()).await?;
    Ok(grants)
}

// ===== 结算与对账 =====

pub async fn settlements(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::Settlement>> {
    let rows = sqlx::query(
        "SELECT id, settlement_no, split_template_id, period_start, period_end, total_cents, status, created_at
         FROM settled_record ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.finance.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::Settlement> {
        Ok(api_contracts::admin::Settlement {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            settlement_no: sqlx::Row::try_get::<String, _>(r, "settlement_no")?,
            split_template_id: sqlx::Row::try_get::<u64, _>(r, "split_template_id")?,
            period_start: sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "period_start")?.to_string(),
            period_end: sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "period_end")?.to_string(),
            total_cents: sqlx::Row::try_get::<i64, _>(r, "total_cents")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

pub async fn reconcile_logs(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::ReconcileLog>> {
    let rows = sqlx::query(
        "SELECT id, reconcile_type, reconcile_date, internal_count, wechat_count, diff_count,
                internal_cents, wechat_cents, diff_cents, resolved, created_at
         FROM finance_reconcile_log ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.finance.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::ReconcileLog> {
        Ok(api_contracts::admin::ReconcileLog {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            reconcile_type: sqlx::Row::try_get::<String, _>(r, "reconcile_type")?,
            reconcile_date: sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "reconcile_date")?.to_string(),
            internal_count: sqlx::Row::try_get::<u32, _>(r, "internal_count")?,
            wechat_count: sqlx::Row::try_get::<u32, _>(r, "wechat_count")?,
            diff_count: sqlx::Row::try_get::<i32, _>(r, "diff_count")?,
            resolved: sqlx::Row::try_get::<i8, _>(r, "resolved")? != 0,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

// ===== refund_task(持久化退款编排,D11) =====

/// `refund_task` 当前状态快照。
pub type TaskRow = (
    String,
    u32,
    Option<String>,
    Option<chrono::NaiveDateTime>,
);

/// 退款列表页展示的任务状态。
pub async fn refund_task_by_no(st: &AppState, no: &str) -> AppResult<Option<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query("SELECT stage,attempts,last_error,scheduled_at FROM refund_task WHERE refund_no=?")
        .bind(no).fetch_optional(st.finance.pool()).await?)
}

/// 从 Redis Stream 事件落地一条退款任务(事件去重靠 `INSERT IGNORE`)。
pub async fn enqueue_refund_task(st: &AppState, no: &str, event_id: &str) -> AppResult<()> {
    sqlx::query("INSERT IGNORE INTO refund_task (refund_no,event_id) VALUES (?,?)")
        .bind(no)
        .bind(event_id)
        .execute(st.finance.pool())
        .await?;
    Ok(())
}

/// 事务内:取一条到期的退款任务并锁住(`FOR UPDATE SKIP LOCKED`)。
pub async fn claim_refund_task(
    tx: &mut common_db::Tx<'_>,
) -> AppResult<Option<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query("SELECT CAST(refund_no AS CHAR CHARACTER SET utf8mb4) AS refund_no,stage,request_json,result_json FROM refund_task WHERE stage IN ('queued','querying','reporting') AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY scheduled_at,refund_no LIMIT 1 FOR UPDATE SKIP LOCKED")
        .fetch_optional(tx.executor()).await?)
}

/// 推进失败:计数 +1、写 last_error、30 秒后重试。
pub async fn defer_refund_task(
    tx: &mut common_db::Tx<'_>,
    no: &str,
    message: &str,
) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET attempts=attempts+1,last_error=?,scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 30 SECOND) WHERE refund_no=?")
        .bind(message).bind(no).execute(tx.executor()).await?;
    Ok(())
}

pub async fn set_refund_stage(tx: &mut common_db::Tx<'_>, no: &str, stage: &str) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET stage=?,last_error=NULL WHERE refund_no=?")
        .bind(stage).bind(no).execute(tx.executor()).await?;
    Ok(())
}

pub async fn set_refund_request(
    tx: &mut common_db::Tx<'_>,
    no: &str,
    request: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET stage='querying',request_json=?,last_error=NULL WHERE refund_no=?")
        .bind(request).bind(no).execute(tx.executor()).await?;
    Ok(())
}

/// 微信查询仍在处理中:计数 +1、60 秒后重查。
pub async fn reschedule_refund_query(tx: &mut common_db::Tx<'_>, no: &str) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 60 SECOND),attempts=attempts+1,last_error=NULL WHERE refund_no=?")
        .bind(no).execute(tx.executor()).await?;
    Ok(())
}

pub async fn set_refund_result(
    tx: &mut common_db::Tx<'_>,
    no: &str,
    result: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET stage='reporting',result_json=?,last_error=NULL WHERE refund_no=?")
        .bind(result).bind(no).execute(tx.executor()).await?;
    Ok(())
}

/// 审核被拒:退款任务转人工,清掉 last_error。
pub async fn mark_refund_manual_review(tx: &mut common_db::Tx<'_>, no: &str) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET stage='manual_review',last_error='退款审核已拒绝' WHERE refund_no=? AND stage IN ('queued','querying','reporting')")
        .bind(no).execute(tx.executor()).await?;
    Ok(())
}

/// 取退款任务并锁住(重试用),返回 `stage` / `last_error` / 是否已到期。
pub async fn lock_refund_task_for_retry(
    tx: &mut common_db::Tx<'_>,
    no: &str,
) -> AppResult<sqlx::mysql::MySqlRow> {
    sqlx::query("SELECT stage,last_error,scheduled_at<=UTC_TIMESTAMP(3) AS due FROM refund_task WHERE refund_no=? FOR UPDATE")
        .bind(no).fetch_optional(tx.executor()).await?
        .ok_or_else(|| AppError::NotFound("退款任务不存在".into()))
}

/// 只唤醒既有任务,**不**重置 provider 请求 / 结果 / 阶段 / 退款单号。
pub async fn wake_refund_task(tx: &mut common_db::Tx<'_>, no: &str) -> AppResult<()> {
    sqlx::query("UPDATE refund_task SET scheduled_at=UTC_TIMESTAMP(3) WHERE refund_no=?")
        .bind(no).execute(tx.executor()).await?;
    Ok(())
}

/// 事务内:退款重试的权限 + 状态判定。返回 `(stage, already_queued)`。
pub async fn load_retry_context(
    tx: &mut common_db::Tx<'_>,
    no: &str,
    admin_user_id: u64,
) -> AppResult<(String, bool)> {
    lock_refund_retry(tx, admin_user_id).await?;
    let row = lock_refund_task_for_retry(tx, no).await?;
    let stage: String = row.try_get("stage")?;
    if !crate::capability::finance::domain::retryable_stage(&stage)
        || row.try_get::<Option<String>, _>("last_error")?.is_none()
    {
        return Err(AppError::Conflict(
            "仅异常中的自动退款任务可重试；已完成或需人工审核的退款不能直接重启".into(),
        ));
    }
    let already_queued = row.try_get::<i32, _>("due")? != 0;
    Ok((stage, already_queued))
}

// ===== audit_log(审计快照) =====

pub async fn audit_refund_create(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    order_id: u64,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','refund.create','charge_order',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(order_id.to_string()).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_refund_decision(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    reject: bool,
    no: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance',?,'refund_record',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(if reject {"refund.reject"} else {"refund.approve"}).bind(no).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_refund_retry(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    no: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','refund.retry','refund_task',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(no).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_invoice_first_approve(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    id: u64,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','invoice.first_approve','invoice_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(id.to_string()).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_invoice_second_approve(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    id: u64,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','invoice.second_approve','invoice_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(id.to_string()).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_invoice_reject(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    id: u64,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','invoice.reject','invoice_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(id.to_string()).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_wallet_risk(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    action: &str,
    id: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance',?,'wallet_refund_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(action).bind(id).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

// ===== invoice_review =====

/// 发票审核队列(admin 侧状态列)。
pub async fn invoice_queue(st: &AppState) -> AppResult<Vec<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query(
        "SELECT invoice_request_id,review_status,reviewed_by,reject_reason,
                first_reviewer_id,first_reviewed_at,second_reviewer_id,second_reviewed_at,invoice_url
         FROM invoice_review ORDER BY id DESC LIMIT 100",
    ).fetch_all(st.finance.pool()).await?)
}

/// 幂等地把申请登记进审核队列(未登记过则置 pending)。
pub async fn ensure_invoice_row(tx: &mut common_db::Tx<'_>, id: u64) -> AppResult<()> {
    sqlx::query("INSERT IGNORE INTO invoice_review (invoice_request_id,review_status) VALUES (?,'pending')")
        .bind(id).execute(tx.executor()).await?;
    Ok(())
}

/// 锁住审核行并取全部判定所需列。
pub async fn lock_invoice_review(
    tx: &mut common_db::Tx<'_>,
    id: u64,
) -> AppResult<sqlx::mysql::MySqlRow> {
    Ok(sqlx::query("SELECT review_status,first_reviewer_id,second_reviewer_id,invoice_url FROM invoice_review WHERE invoice_request_id=? FOR UPDATE")
        .bind(id).fetch_one(tx.executor()).await?)
}

/// 锁住审核行(拒绝路径只需三列)。
pub async fn lock_invoice_review_for_reject(
    tx: &mut common_db::Tx<'_>,
    id: u64,
) -> AppResult<sqlx::mysql::MySqlRow> {
    Ok(sqlx::query("SELECT review_status,reviewed_by,reject_reason FROM invoice_review WHERE invoice_request_id=? FOR UPDATE")
        .bind(id).fetch_one(tx.executor()).await?)
}

pub async fn mark_invoice_awaiting_second(
    tx: &mut common_db::Tx<'_>,
    id: u64,
    admin_user_id: u64,
    invoice_url: &str,
) -> AppResult<()> {
    sqlx::query("UPDATE invoice_review SET review_status='awaiting_second',first_reviewer_id=?,first_reviewed_at=UTC_TIMESTAMP(3),invoice_url=? WHERE invoice_request_id=?")
        .bind(admin_user_id).bind(invoice_url).bind(id).execute(tx.executor()).await?;
    Ok(())
}

pub async fn mark_invoice_approved(
    tx: &mut common_db::Tx<'_>,
    id: u64,
    admin_user_id: u64,
) -> AppResult<()> {
    sqlx::query("UPDATE invoice_review SET review_status='approved',second_reviewer_id=?,second_reviewed_at=UTC_TIMESTAMP(3),reviewed_by=?,reviewed_at=UTC_TIMESTAMP(3),reject_reason=NULL WHERE invoice_request_id=?")
        .bind(admin_user_id).bind(admin_user_id).bind(id).execute(tx.executor()).await?;
    Ok(())
}

pub async fn mark_invoice_rejected(
    tx: &mut common_db::Tx<'_>,
    id: u64,
    admin_user_id: u64,
    reason: &str,
) -> AppResult<()> {
    sqlx::query("UPDATE invoice_review SET review_status='rejected',reviewed_by=?,reviewed_at=UTC_TIMESTAMP(3),reject_reason=? WHERE invoice_request_id=?")
        .bind(admin_user_id).bind(reason).bind(id).execute(tx.executor()).await?;
    Ok(())
}

/// 事务内:确认首签人账号仍有效且未撤销发票审核权限。
pub async fn lock_first_invoice_reviewer(
    tx: &mut common_db::Tx<'_>,
    first_id: u64,
) -> AppResult<bool> {
    let first_active: Option<u64> = sqlx::query_scalar(
        "SELECT a.id FROM admin_user_role a
         JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id
         JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL
           AND r.code='customer_finance' AND p.code='invoice.review' FOR SHARE",
    ).bind(first_id).fetch_optional(tx.executor()).await?;
    Ok(first_active.is_some())
}
