//! finance 域纯逻辑(零 I/O)
//!
//! 校验与"能不能做这笔操作"的判定都在这里,SQL 与跨服务调用在 repository / usecase。
//! 判定顺序与中文文案一字未改。

use common_error::{AppError, AppResult};

/// 退款单号与审核意见的合法性(D14 的输入闸)。
///
/// 单号只允许字母数字与 `_-|*@`(与微信退款单号兼容),长度上限 64;
/// 意见非空、≤255 字、不含控制字符。
pub fn valid_refund_no(no: &str) -> bool {
    !no.is_empty()
        && no.len() <= 64
        && no.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_-|*@".contains(&b))
}

/// 退款审核意见的合法性。
pub fn valid_review_comment(comment: &str) -> bool {
    !comment.trim().is_empty()
        && comment.chars().count() <= 255
        && !comment.chars().any(char::is_control)
}

/// 重试原因 / 审核意见的合并校验。任一不合法都返回同一条文案。
pub fn valid_refund_decision(no: &str, comment: &str) -> AppResult<()> {
    if !valid_refund_no(no) || !valid_review_comment(comment) {
        return Err(AppError::BadRequest("退款单号或审核意见无效".into()));
    }
    Ok(())
}

/// 重试原因 + 退款单号的合并校验。
pub fn valid_refund_retry(no: &str, reason: &str) -> AppResult<()> {
    if !valid_refund_no(no)
        || reason.trim().is_empty()
        || reason.chars().count() > 255
        || reason.chars().any(char::is_control)
    {
        return Err(AppError::BadRequest("退款单号或重试原因无效".into()));
    }
    Ok(())
}

/// 钱包风控审核意见的合法性(与退款审核同一口径,文案不同)。
pub fn valid_wallet_risk_comment(comment: &str) -> AppResult<()> {
    if comment.trim().is_empty() || comment.chars().count() > 255 || comment.chars().any(char::is_control) {
        return Err(AppError::BadRequest("钱包风控处理意见无效".into()));
    }
    Ok(())
}

/// 发票链接必须是有效 HTTPS(开票后必须提供)。
pub fn valid_invoice_url(url: &str) -> bool {
    url.len() <= 512
        && reqwest::Url::parse(url)
            .is_ok_and(|u| u.scheme() == "https" && u.host_str().is_some())
}

/// 开票后必须提供有效 HTTPS 发票链接。
pub fn ensure_invoice_url(url: &str) -> AppResult<()> {
    if !valid_invoice_url(url) {
        return Err(AppError::BadRequest("开票后必须提供有效 HTTPS 发票链接".into()));
    }
    Ok(())
}

/// 拒绝发票申请的原因约束:必填、≤255 字、无控制字符。
pub fn ensure_invoice_reject_reason(reason: &str) -> AppResult<()> {
    if reason.trim().is_empty() || reason.chars().count() > 255 || reason.chars().any(char::is_control) {
        return Err(AppError::BadRequest("拒绝原因必须填写且最多 255 字".into()));
    }
    Ok(())
}

/// 钱包风控审核人申请编号必须是 UUID。
pub fn normalize_wallet_risk_id(id: &str) -> AppResult<String> {
    uuid::Uuid::parse_str(id)
        .map(|u| u.to_string())
        .map_err(|_| AppError::BadRequest("申请编号无效".into()))
}

/// `refund_task` 处于可重试的自动阶段。
pub fn retryable_stage(stage: &str) -> bool {
    ["queued", "querying", "reporting"].contains(&stage)
}

/// 自动退款任务只有在"异常中"时才可重试:阶段可重试 **且** 有 last_error。
/// 已完成或需人工审核的退款不能直接重启。
pub fn ensure_retryable(stage: &str, has_error: bool) -> AppResult<()> {
    if !retryable_stage(stage) || !has_error {
        return Err(AppError::Conflict(
            "仅异常中的自动退款任务可重试；已完成或需人工审核的退款不能直接重启".into(),
        ));
    }
    Ok(())
}

/// 微信退款查询返回的状态如何落 `refund_task.stage`。
///
/// `SUCCESS` 收尾,其余一律转人工 —— 终态失败不应让后台任务空转。
pub fn stage_from_provider_status(status: &str) -> &'static str {
    if status == "SUCCESS" { "done" } else { "manual_review" }
}

/// 跨服务退款结果回报后 `refund_task.stage` 的取值。
pub fn stage_from_reported_success(success: bool) -> &'static str {
    if success { "done" } else { "manual_review" }
}

/// 退款列表里"是否可审核"的分派判定。
///
/// 需同时满足:有审核权限、订单处于 pending、业务类型是充电单、
/// 审核态不是已通过、且当前操作人不是首签人。
#[allow(clippy::too_many_arguments)]
pub fn can_approve(
    can_review: bool,
    status: &str,
    biz_type: &str,
    review_status: Option<&str>,
    first_signer: Option<&str>,
    me: &str,
) -> bool {
    can_review
        && status == "pending"
        && biz_type == "charge"
        && review_status != Some("approved")
        && first_signer != Some(me)
}

/// 退款列表里"是否可拒绝"的分派判定:仅当审核态为 `awaiting_second`。
pub fn can_reject(can_review: bool, status: &str, biz_type: &str, review_status: Option<&str>) -> bool {
    can_review && status == "pending" && biz_type == "charge" && review_status == Some("awaiting_second")
}

/// 订单详情里是否显示"发起退款"入口。
pub fn can_start_refund(grants: i64, status: &str, paid_cents: Option<i64>) -> bool {
    grants == 2
        && ["completed", "failed", "cancelled"].contains(&status)
        && paid_cents.unwrap_or(0) > 0
}
