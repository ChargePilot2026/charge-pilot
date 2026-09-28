//! order 域纯逻辑(零 I/O)
//!
//! 本文件只放**不碰数据库、不碰 Redis、不碰 HTTP** 的判定与计算,
//! 因此可以脱离环境直接单测。SQL 一律在同目录 `repository_sql.rs`。

use api_contracts::{ChargeEndRequest, QuoteResponse, StartResultRequest};
use common_error::{AppError, AppResult};

/// 设备启动 ACK 的入参校验(原 `charge_start::validate`)。
///
/// 判定顺序与错误文案逐字保留。
pub fn start_validate(path: &str, req: &StartResultRequest) -> AppResult<String> {
    if path != req.order_no
        || path.is_empty()
        || path.len() > 64
        || req.device_id.is_empty()
        || req.device_id.len() > 64
        || req.port_no == 0
        || req.port_id == Some(0)
        || (req.success && req.port_id.is_none())
        || req
            .error
            .as_ref()
            .is_some_and(|e| e.chars().count() > 255 || e.chars().any(char::is_control))
    {
        return Err(AppError::BadRequest(
            "设备启动结果缺少有效的订单或端口信息".into(),
        ));
    }
    uuid::Uuid::parse_str(&req.command_id)
        .map(|id| id.to_string())
        .map_err(|_| AppError::BadRequest("启动指令标识无效".into()))
}

pub fn start_conflict() -> AppError {
    AppError::Conflict("启动结果与订单或端口占用不一致".into())
}

pub fn end_conflict() -> AppError {
    AppError::Conflict("充电结束确认与订单不一致".into())
}

pub fn fee_conflict() -> AppError {
    AppError::Conflict("实结费用与订单、计量或退款记录不一致".into())
}

/// 充电结束上报的入参前置校验部分(与 SQL 无关的那一半)。
///
/// 返回 `Ok(())` 表示可以继续进入事务内的锁定与比对。
pub fn end_validate(order: &str, req: &ChargeEndRequest) -> AppResult<()> {
    let start = uuid::Uuid::parse_str(&req.start_command_id)
        .map_err(|_| end_conflict())?
        .to_string();
    let stop = uuid::Uuid::parse_str(&req.stop_command_id)
        .map_err(|_| end_conflict())?
        .to_string();
    if order != req.order_no
        || req.port_id == 0
        || req.port_no == 0
        || req.meter.charged_wh > 100_000_000
        || req.meter.charged_seconds > 604800
        || req.meter.ended_at > chrono::Utc::now() + chrono::Duration::minutes(5)
    {
        return Err(end_conflict());
    }
    let _ = (start, stop);
    Ok(())
}

/// 校验计费服务返回的金额三元组,并返回可下发给微信的 i32 支付金额。
///
/// 判定顺序与错误文案保持原样:先看符号与加和一致,再试 `i32` 转换。
pub fn validate_quote(quote: &QuoteResponse) -> AppResult<i32> {
    if quote.electric_cents < 0 || quote.service_cents < 0 || quote.total_cents <= 0
        || quote.electric_cents.checked_add(quote.service_cents) != Some(quote.total_cents)
    {
        return Err(AppError::ServiceUnavailable("计费服务返回了无效金额".into()));
    }
    i32::try_from(quote.total_cents)
        .map_err(|_| AppError::ServiceUnavailable("支付金额超出支持范围".into()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_inconsistent_or_unpayable_quotes() {
        for (total, electric, service) in [(0, 0, 0), (-1, 0, -1), (10, 5, 6), (10, -1, 11),
            (i64::MAX, i64::MAX, 1), (i32::MAX as i64 + 1, i32::MAX as i64 + 1, 0)] {
            assert!(validate_quote(&QuoteResponse { total_cents: total, electric_cents: electric, service_cents: service }).is_err());
        }
        assert_eq!(validate_quote(&QuoteResponse { total_cents: 123, electric_cents: 100, service_cents: 23 }).unwrap(), 123);
    }

    fn start_request() -> StartResultRequest {
        StartResultRequest {
            order_no: "ORD-1".into(),
            command_id: uuid::Uuid::new_v4().to_string(),
            device_id: "DEV-1".into(),
            port_no: 1,
            port_id: Some(9_000_000_001),
            success: true,
            error: None,
        }
    }

    #[test]
    fn start_validation_rejects_mismatched_or_incomplete_results() {
        let req = start_request();
        // path 与 order_no 必须一致
        assert!(start_validate("OTHER", &req).is_err());
        assert!(start_validate("", &req).is_err());
        // 成功启动必须给出 port_id
        let mut missing_port = req.clone();
        missing_port.port_id = None;
        assert!(start_validate("ORD-1", &missing_port).is_err());
        // port_id=0 无效
        let mut zero_port = req.clone();
        zero_port.port_id = Some(0);
        assert!(start_validate("ORD-1", &zero_port).is_err());
        // command_id 必须是 UUID
        let mut bad_command = req.clone();
        bad_command.command_id = "not-a-uuid".into();
        assert!(start_validate("ORD-1", &bad_command).is_err());
        // 正常路径返回规范化后的 command_id
        assert_eq!(start_validate("ORD-1", &req).unwrap(), req.command_id);
    }
}
