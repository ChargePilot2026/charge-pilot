//! 微信支付 + 小程序能力封装(技术规格 § 9.4 + § 9.6)
//!
//! - code2Session: 微信 code → openid + session_key
//! - pay_jsapi: 下单 + 签名 + 回调验签
//! - refund: 调微信退款 V3 API + 重试(1s/5s/30s/2min,见技术规格 § 7.6)

use base64::Engine;
use common_auth::{constant_time_eq, verify_wechat_v3_signature};
use common_config::WechatConfig;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::time::Duration;

/// 微信登录(code2Session)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Code2SessionResp {
    #[serde(default)]
    pub openid: String,
    pub session_key: Option<String>,
    pub unionid: Option<String>,
    #[serde(default)]
    pub errcode: i32,
    #[serde(default)]
    pub errmsg: String,
}

pub async fn code2session(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    code: &str,
) -> AppResult<Code2SessionResp> {
    if code.is_empty() || code.len() > 256 || code.chars().any(|c| c.is_whitespace() || c.is_control()) {
        return Err(AppError::BadRequest("微信登录凭证无效".into()));
    }
    // Do not include request errors/URLs: the query contains the app secret.
    let response = http.get("https://api.weixin.qq.com/sns/jscode2session")
        .query(&[("appid",cfg.appid.as_str()),("secret",cfg.secret.as_str()),("js_code",code),("grant_type","authorization_code")])
        .timeout(Duration::from_secs(5)).send().await
        .map_err(|_| AppError::ServiceUnavailable("微信登录服务连接失败".into()))?;
    if !response.status().is_success() {
        return Err(AppError::ServiceUnavailable("微信登录服务暂时不可用".into()));
    }
    let body: Code2SessionResp = response.json().await
        .map_err(|_| AppError::ServiceUnavailable("微信登录响应格式错误".into()))?;
    validate_session(body)
}

fn validate_session(body: Code2SessionResp) -> AppResult<Code2SessionResp> {
    if body.errcode != 0 {
        return Err(AppError::WechatPayFailed(format!("微信登录失败（错误码 {}），请重新登录",body.errcode)));
    }
    if body.openid.is_empty() || body.openid.len() > 64 || body.session_key.as_deref().map_or(true, str::is_empty) {
        return Err(AppError::ServiceUnavailable("微信登录响应缺少身份信息".into()));
    }
    Ok(body)
}

/// JSAPI 预下单请求体(V3)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsapiOrderReq {
    pub appid: String,
    pub mchid: String,
    pub description: String,
    pub out_trade_no: String,
    pub time_expire: String,
    pub attach: Option<String>,
    pub notify_url: String,
    pub amount: JsapiAmount,
    pub payer: JsapiPayer,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsapiAmount {
    pub total: i32,        // 单位:分
    pub currency: String,  // CNY
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsapiPayer {
    pub openid: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsapiOrderResp {
    pub prepay_id: String,
}

/// 调用 JSAPI 下单(简化:实际项目需在 Authorization header 注入 V3 签名)
pub async fn jsapi_create_order(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &JsapiOrderReq,
) -> AppResult<JsapiOrderResp> {
    let url = format!("{}/v3/pay/transactions/jsapi", cfg.pay_base_url);
    // 简化:真实场景需要构造 Authorization 头(merchant id + nonce + timestamp + signature)
    // 完整签名详见微信 V3 文档。本项目部署文档提供完整示例。
    let auth = format!(
        "WECHATPAY2-SHA256-RSA2048 mchid=\"{}\",nonce_str=\"{}\",timestamp=\"{}\",serial_no=\"pending-real-cert\",signature=\"pending-real-sign\"",
        cfg.mch_id,
        uuid::Uuid::new_v4(),
        chrono::Utc::now().timestamp()
    );
    let resp = http
        .post(&url)
        .header("Authorization", auth)
        .header("Content-Type", "application/json")
        .header("Accept", "application/json")
        .json(req)
        .send()
        .await?;
    if !resp.status().is_success() {
        return Err(AppError::WechatPayFailed(format!(
            "jsapi_create_order status={}",
            resp.status()
        )));
    }
    let body: JsapiOrderResp = resp.json().await?;
    Ok(body)
}

/// 给前端 `wx.requestPayment` 的签名参数
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsapiPaySign {
    pub appId: String,
    pub timeStamp: String,
    pub nonceStr: String,
    pub package: String,   // prepay_id=xxx
    pub signType: String,  // RSA
    pub paySign: String,
}

/// 生成前端支付签名(简化:真实场景需要用私钥签名)
pub fn sign_jsapi_pay(cfg: &WechatConfig, prepay_id: &str) -> JsapiPaySign {
    let time_stamp = chrono::Utc::now().timestamp().to_string();
    let nonce_str = uuid::Uuid::new_v4().to_string();
    let package = format!("prepay_id={prepay_id}");
    // 真实签法: appId\ntimeStamp\nnonceStr\npackage\n = 用商户私钥 SHA256withRSA
    // 这里占位,提示项目需配置真实签名链路。
    let pay_sign = "PENDING-RSA-SIGNATURE".to_string();
    JsapiPaySign {
        appId: cfg.appid.clone(),
        timeStamp: time_stamp,
        nonceStr: nonce_str,
        package,
        signType: "RSA".into(),
        paySign: pay_sign,
    }
}

/// 退款请求(V3)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundReq {
    pub transaction_id: Option<String>,
    pub out_trade_no: Option<String>,
    pub out_refund_no: String,
    pub reason: Option<String>,
    pub notify_url: Option<String>,
    pub funds_account: Option<String>,
    pub amount: RefundAmount,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundAmount {
    pub refund: i32,
    pub total: i32,
    pub currency: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundResp {
    pub refund_id: String,
    pub out_refund_no: String,
    pub status: String,   // PROCESSING / SUCCESS / ABNORMAL
    pub channel: Option<String>,
}

/// 退款调用 + 指数退避(技术规格 § 7.6: 1s/5s/30s/2min)
pub async fn refund_with_retry(
    http: &reqwest::Client,
    cfg: &WechatConfig,
    req: &RefundReq,
) -> AppResult<RefundResp> {
    let backoff = [Duration::from_secs(1), Duration::from_secs(5), Duration::from_secs(30), Duration::from_secs(120)];
    let url = cfg.refund_url.clone();
    let auth = format!(
        "WECHATPAY2-SHA256-RSA2048 mchid=\"{}\",nonce_str=\"{}\",timestamp=\"{}\",serial_no=\"pending-real-cert\",signature=\"pending-real-sign\"",
        cfg.mch_id,
        uuid::Uuid::new_v4(),
        chrono::Utc::now().timestamp()
    );
    let mut last_err: Option<AppError> = None;
    for (i, delay) in backoff.iter().enumerate() {
        if i > 0 {
            tokio::time::sleep(*delay).await;
        }
        match http
            .post(&url)
            .header("Authorization", &auth)
            .header("Content-Type", "application/json")
            .json(req)
            .send()
            .await
        {
            Ok(resp) => {
                let status = resp.status();
                if status.is_success() {
                    let body: RefundResp = resp.json().await?;
                    if body.status == "SUCCESS" || body.status == "PROCESSING" {
                        return Ok(body);
                    }
                    last_err = Some(AppError::WechatRefundFailed(format!(
                        "refund status={}",
                        body.status
                    )));
                } else {
                    let txt = resp.text().await.unwrap_or_default();
                    last_err = Some(AppError::WechatRefundFailed(format!("status={} body={}", status, txt)));
                }
            }
            Err(e) => last_err = Some(AppError::HttpClient(format!("refund http: {e}"))),
        }
    }
    Err(last_err.unwrap_or_else(|| AppError::WechatRefundFailed("refund exhausted retries".into())))
}

/// 验签微信回调(技术规格 § 9.4)
pub fn verify_callback_signature(
    cfg: &WechatConfig,
    payload: &str,
    timestamp: &str,
    nonce: &str,
    signature_b64: &str,
) -> bool {
    verify_wechat_v3_signature(payload, timestamp, nonce, signature_b64, &cfg.pay_key)
}

/// 解密微信回调中的 resource.ciphertext(AES-256-GCM)
pub fn decrypt_callback_resource(ciphertext_b64: &str, key_b64: &str, _nonce_b64: &str, _aad: &str) -> AppResult<String> {
    // 实际使用 AES-256-GCM 解密;这里给占位 + 文档提示。
    // 项目部署后请参考微信 V3 文档实现:key 用 APIv3 key 的 32 字节。
    let _engine = base64::engine::general_purpose::STANDARD;
    let _bytes = base64::engine::general_purpose::STANDARD
        .decode(ciphertext_b64)
        .map_err(|e| AppError::Internal(format!("base64 decode: {e}")))?;
    let _key = base64::engine::general_purpose::STANDARD
        .decode(key_b64)
        .map_err(|e| AppError::Internal(format!("key base64: {e}")))?;
    Err(AppError::Internal(
        "AES-256-GCM decrypt not yet implemented (production deploy-time task)".into(),
    ))
}

/// 小程序客服入口签名(技术规格 § 9.6)
pub fn customer_service_entry(_cfg: &WechatConfig, _order_id: &str, _openid: &str) -> String {
    // 实际部署需调用 https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=...
    "PENDING-CS-ENTRY".into()
}

/// HMAC 工具:openid 签名(确保回调合法)
pub fn hmac_sha256_b64(key: &[u8], msg: &[u8]) -> String {
    use hmac::{Hmac, Mac};
    let mut mac = Hmac::<Sha256>::new_from_slice(key).unwrap();
    mac.update(msg);
    let r = mac.finalize().into_bytes();
    base64::engine::general_purpose::STANDARD.encode(r)
}

/// 短字符串相等(避免时序攻击)
pub fn _eq(a: &str, b: &str) -> bool {
    constant_time_eq(a.as_bytes(), b.as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hmac_b64_roundtrip() {
        let a = hmac_sha256_b64(b"k", b"m");
        let b = hmac_sha256_b64(b"k", b"m");
        assert_eq!(a, b);
    }
}
#[cfg(test)]
mod login_response_tests {
    use super::*;
    #[test]
    fn accepts_success_without_error_fields_and_rejects_error_without_openid() {
        let success=serde_json::from_str(r#"{"openid":"test-openid","session_key":"test-session"}"#).unwrap();
        assert_eq!(validate_session(success).unwrap().openid,"test-openid");
        let failure=serde_json::from_str(r#"{"errcode":40029,"errmsg":"invalid code"}"#).unwrap();
        assert_eq!(validate_session(failure).unwrap_err().code(),3001);
        assert!(validate_session(serde_json::from_str("{}").unwrap()).is_err());
    }
}
