//! Verify the unmodified HTTP body before parsing or decrypting any payment fields.
use base64::{engine::general_purpose::STANDARD, Engine};
use common_config::WechatConfig;
use common_error::{AppError, AppResult};
use ring::aead::{Aad, LessSafeKey, Nonce, UnboundKey, AES_256_GCM};
use serde::{Deserialize, Serialize};

#[derive(Deserialize)]
struct Envelope {
    event_type: String,
    resource_type: String,
    resource: Resource,
}
#[derive(Deserialize)]
struct Resource {
    original_type: String,
    algorithm: String,
    ciphertext: String,
    nonce: String,
    associated_data: Option<String>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct PaymentNotification {
    pub appid: String,
    pub mchid: String,
    pub out_trade_no: String,
    pub transaction_id: String,
    pub trade_type: String,
    pub trade_state: String,
    pub success_time: chrono::DateTime<chrono::FixedOffset>,
    pub payer: Payer,
    pub amount: Amount,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Payer {
    pub openid: String,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Amount {
    pub total: i64,
    pub payer_total: i64,
    pub currency: String,
    pub payer_currency: String,
}

fn invalid() -> AppError {
    AppError::BadRequest("微信支付通知内容无效".into())
}

fn decrypt(resource: &Resource, key: &str) -> AppResult<Vec<u8>> {
    if key.as_bytes().len() != 32 {
        return Err(AppError::Config("微信 APIv3 密钥必须为 32 字节".into()));
    }
    if resource.algorithm != "AEAD_AES_256_GCM" || resource.original_type != "transaction" {
        return Err(invalid());
    }
    let key =
        LessSafeKey::new(UnboundKey::new(&AES_256_GCM, key.as_bytes()).map_err(|_| invalid())?);
    let nonce =
        Nonce::try_assume_unique_for_key(resource.nonce.as_bytes()).map_err(|_| invalid())?;
    let mut ciphertext = STANDARD
        .decode(&resource.ciphertext)
        .map_err(|_| invalid())?;
    let plain = key
        .open_in_place(
            nonce,
            Aad::from(resource.associated_data.as_deref().unwrap_or("").as_bytes()),
            &mut ciphertext,
        )
        .map_err(|_| invalid())?;
    Ok(plain.to_vec())
}

pub fn decode_payment_notification(
    cfg: &WechatConfig,
    headers: &reqwest::header::HeaderMap,
    body: &[u8],
) -> AppResult<PaymentNotification> {
    let raw = std::str::from_utf8(body).map_err(|_| invalid())?;
    // Business errors normally use HTTP 200; callback signature failures must not ACK.
    crate::pay_signing::verify_response(cfg, headers, raw).map_err(|err| match err {
        AppError::Config(_) => err,
        _ => AppError::Unauthorized("微信支付通知验签失败".into()),
    })?;
    let envelope: Envelope = serde_json::from_str(raw).map_err(|_| invalid())?;
    if envelope.event_type != "TRANSACTION.SUCCESS" || envelope.resource_type != "encrypt-resource"
    {
        return Err(invalid());
    }
    let notification: PaymentNotification =
        serde_json::from_slice(&decrypt(&envelope.resource, &cfg.pay_key)?)
            .map_err(|_| invalid())?;
    if notification.appid != cfg.appid
        || notification.mchid != cfg.mch_id
        || notification.trade_type != "JSAPI"
        || notification.trade_state != "SUCCESS"
        || notification.amount.currency != "CNY"
        || notification.amount.payer_currency != "CNY"
        || notification.amount.total <= 0
        || notification.amount.total > i32::MAX as i64
        || notification.amount.payer_total < 0
        || notification.amount.payer_total > notification.amount.total
        || notification.success_time.timestamp() > chrono::Utc::now().timestamp() + 300
        || [
            &notification.out_trade_no,
            &notification.transaction_id,
            &notification.payer.openid,
        ]
        .iter()
        .any(|v| {
            v.is_empty() || v.len() > 64 || v.chars().any(|c| c.is_whitespace() || c.is_control())
        })
    {
        return Err(invalid());
    }
    Ok(notification)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn authenticates_ciphertext_aad_nonce_and_raw_key() {
        let key = "12345678901234567890123456789012";
        let nonce = "123456789012";
        let mut data = br#"{"transaction_id":"test"}"#.to_vec();
        LessSafeKey::new(UnboundKey::new(&AES_256_GCM, key.as_bytes()).unwrap())
            .seal_in_place_append_tag(
                Nonce::try_assume_unique_for_key(nonce.as_bytes()).unwrap(),
                Aad::from(b"transaction"),
                &mut data,
            )
            .unwrap();
        let mut r = Resource {
            original_type: "transaction".into(),
            algorithm: "AEAD_AES_256_GCM".into(),
            ciphertext: STANDARD.encode(&data),
            nonce: nonce.into(),
            associated_data: Some("transaction".into()),
        };
        assert_eq!(decrypt(&r, key).unwrap(), br#"{"transaction_id":"test"}"#);
        assert!(decrypt(&r, "wrong").is_err());
        r.associated_data = None;
        assert!(decrypt(&r, key).is_err());
        r.associated_data = Some("transaction".into());
        r.nonce = "abcdefghijkl".into();
        assert!(decrypt(&r, key).is_err());
        r.nonce = nonce.into();
        data[0] ^= 1;
        r.ciphertext = STANDARD.encode(data);
        assert!(decrypt(&r, key).is_err());
        r.ciphertext = "invalid-base64".into();
        assert!(decrypt(&r, key).is_err());
    }
}
