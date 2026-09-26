//! API v3 refund requests use merchant RSA signatures and authenticated raw responses.
use crate::{pay_signing, RefundReq, RefundResp};
use common_config::WechatConfig;
use common_error::{AppError, AppResult};
use std::time::Duration;

fn valid_id(value: &str, max: usize) -> bool {
    !value.is_empty()
        && value.len() <= max
        && value
            .bytes()
            .all(|v| v.is_ascii_alphanumeric() || b"_-|*@".contains(&v))
}
fn validate(req: &RefundReq) -> AppResult<()> {
    if !valid_id(&req.out_refund_no, 64)
        || req.transaction_id.is_some() == req.out_trade_no.is_some()
        || req
            .transaction_id
            .as_ref()
            .is_some_and(|v| !valid_id(v, 32))
        || req.out_trade_no.as_ref().is_some_and(|v| !valid_id(v, 32))
        || req.amount.refund <= 0
        || req.amount.total < req.amount.refund
        || req.amount.currency != "CNY"
        || req
            .reason
            .as_ref()
            .is_some_and(|s| s.len() > 80 || s.chars().any(char::is_control))
        || req
            .funds_account
            .as_deref()
            .is_some_and(|s| !["AVAILABLE", "UNSETTLED"].contains(&s))
    {
        return Err(AppError::BadRequest("微信退款标识或金额无效".into()));
    }
    if let Some(notify) = &req.notify_url {
        let url = reqwest::Url::parse(notify)
            .map_err(|_| AppError::BadRequest("退款通知地址无效".into()))?;
        if notify.len() > 256
            || url.scheme() != "https"
            || url.host_str().is_none()
            || url.query().is_some()
            || url.fragment().is_some()
            || !url.username().is_empty()
            || url.password().is_some()
        {
            return Err(AppError::BadRequest("退款通知地址无效".into()));
        }
    }
    Ok(())
}
fn verify_match(req: &RefundReq, resp: RefundResp) -> AppResult<RefundResp> {
    if !valid_id(&resp.refund_id, 64)
        || resp.out_refund_no != req.out_refund_no
        || req
            .transaction_id
            .as_ref()
            .is_some_and(|v| v != &resp.transaction_id)
        || req
            .out_trade_no
            .as_ref()
            .is_some_and(|v| v != &resp.out_trade_no)
        || resp.amount.refund != req.amount.refund
        || resp.amount.total != req.amount.total
        || resp.amount.currency != "CNY"
        || !["SUCCESS", "PROCESSING", "CLOSED", "ABNORMAL"].contains(&resp.status.as_str())
    {
        return Err(AppError::WechatRefundFailed(
            "微信退款响应订单、金额或状态不匹配".into(),
        ));
    }
    Ok(resp)
}
async fn send(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &RefundReq,
    query: bool,
) -> AppResult<RefundResp> {
    validate(req)?;
    pay_signing::validate(cfg)?;
    let mut url = reqwest::Url::parse(&cfg.refund_url)
        .map_err(|_| AppError::Config("微信退款地址无效".into()))?;
    // Loopback HTTP supports deterministic local protocol tests only.
    if (url.scheme() != "https"
        && !(url.scheme() == "http"
            && matches!(url.host_str(), Some("127.0.0.1" | "localhost" | "[::1]"))))
        || url.query().is_some()
        || url.fragment().is_some()
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return Err(AppError::Config("微信退款地址必须为 HTTPS".into()));
    }
    let method = if query {
        reqwest::Method::GET
    } else {
        reqwest::Method::POST
    };
    if query {
        url.path_segments_mut()
            .map_err(|_| AppError::Config("微信退款地址无效".into()))?
            .pop_if_empty()
            .push(&req.out_refund_no);
    }
    let body = if query {
        String::new()
    } else {
        serde_json::to_string(req)?
    };
    let auth = pay_signing::authorization(
        cfg,
        method.as_str(),
        &url,
        &body,
        &chrono::Utc::now().timestamp().to_string(),
        &uuid::Uuid::new_v4().simple().to_string(),
    )?;
    let response = http
        .request(method, url)
        .header("Authorization", auth)
        .header("Content-Type", "application/json")
        .header("Accept", "application/json")
        .body(body)
        .timeout(Duration::from_secs(10))
        .send()
        .await
        .map_err(|_| {
            AppError::ServiceUnavailable("微信退款连接结果不确定，需按原退款单号查询或重试".into())
        })?;
    let status = response.status();
    let headers = response.headers().clone();
    let raw = response
        .text()
        .await
        .map_err(|_| AppError::ServiceUnavailable("微信退款响应读取失败".into()))?;
    // Do not trust any business response, even a non-2xx response, before verification.
    pay_signing::verify_response(cfg, &headers, &raw)?;
    if !status.is_success() {
        if query
            && status.as_u16() == 404
            && serde_json::from_str::<serde_json::Value>(&raw)
                .ok()
                .and_then(|v| v.get("code").and_then(|c| c.as_str()).map(str::to_owned))
                .as_deref()
                == Some("RESOURCE_NOT_EXISTS")
        {
            return Err(AppError::NotFound("微信退款单不存在".into()));
        }
        return if status.is_server_error() || status.as_u16() == 429 {
            Err(AppError::ServiceUnavailable(
                "微信退款暂不可用，请按原退款单号重试".into(),
            ))
        } else {
            Err(AppError::WechatRefundFailed(format!(
                "微信退款请求未受理（HTTP {}）",
                status.as_u16()
            )))
        };
    }
    let result = serde_json::from_str(&raw)
        .map_err(|_| AppError::WechatRefundFailed("微信退款响应格式无效".into()))?;
    verify_match(req, result)
}
pub async fn refund_once(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &RefundReq,
) -> AppResult<RefundResp> {
    send(http, cfg, req, false).await
}
pub async fn query_refund(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &RefundReq,
) -> AppResult<RefundResp> {
    send(http, cfg, req, true).await
}
pub async fn refund_with_retry(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &RefundReq,
) -> AppResult<RefundResp> {
    let delays = [1, 5, 30, 120];
    for attempt in 0..=delays.len() {
        match refund_once(http, cfg, req).await {
            Err(AppError::ServiceUnavailable(_)) if attempt < delays.len() => {
                tokio::time::sleep(Duration::from_secs(delays[attempt])).await
            }
            result => return result,
        }
    }
    unreachable!()
}
#[cfg(test)]
mod tests {
    use super::*;
    fn request() -> RefundReq {
        RefundReq {
            transaction_id: Some("42000000001".into()),
            out_trade_no: None,
            out_refund_no: "REF_test".into(),
            reason: None,
            notify_url: None,
            funds_account: None,
            amount: crate::RefundAmount {
                refund: 82,
                total: 100,
                currency: "CNY".into(),
            },
        }
    }
    fn response() -> RefundResp {
        RefundResp {
            refund_id: "5000000001".into(),
            out_refund_no: "REF_test".into(),
            status: "PROCESSING".into(),
            channel: None,
            transaction_id: "42000000001".into(),
            out_trade_no: "PAY_test".into(),
            amount: crate::RefundAmount {
                refund: 82,
                total: 100,
                currency: "CNY".into(),
            },
        }
    }
    #[test]
    fn rejects_invalid_amounts_and_ambiguous_identity() {
        let mut req = request();
        assert!(validate(&req).is_ok());
        assert!(serde_json::to_value(&req)
            .unwrap()
            .get("out_trade_no")
            .is_none());
        req.out_trade_no = Some("PAY_test".into());
        assert!(validate(&req).is_err());
        req.out_trade_no = None;
        req.amount.refund = 101;
        assert!(validate(&req).is_err());
    }
    #[test]
    fn binds_response_to_refund_and_keeps_processing_distinct() {
        assert_eq!(
            verify_match(&request(), response()).unwrap().status,
            "PROCESSING"
        );
        let mut r = response();
        r.amount.refund = 83;
        assert!(verify_match(&request(), r).is_err());
        let mut r = response();
        r.out_refund_no = "other".into();
        assert!(verify_match(&request(), r).is_err());
        let mut r = response();
        r.status = "UNKNOWN".into();
        assert!(verify_match(&request(), r).is_err());
    }
}
