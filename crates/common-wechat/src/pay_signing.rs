//! WeChat API v3 merchant RSA signing and pinned platform-public-key verification.
use base64::{engine::general_purpose::STANDARD, Engine};
use common_config::WechatConfig;
use common_error::{AppError, AppResult};
use rsa::{
    pkcs1::DecodeRsaPrivateKey,
    pkcs8::{DecodePrivateKey, DecodePublicKey},
    traits::PublicKeyParts,
    Pkcs1v15Sign, RsaPrivateKey, RsaPublicKey,
};
use sha2::{Digest, Sha256};

fn required<'a>(v: &'a Option<String>, name: &str) -> AppResult<&'a str> {
    v.as_deref()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| AppError::Config(format!("缺少微信支付配置 {name}")))
}
fn private_key(cfg: &WechatConfig) -> AppResult<RsaPrivateKey> {
    let pem = std::fs::read_to_string(required(&cfg.private_key_path, "WECHAT_PRIVATE_KEY_PATH")?)
        .map_err(|_| AppError::Config("无法读取微信商户私钥".into()))?;
    let key = RsaPrivateKey::from_pkcs8_pem(&pem)
        .or_else(|_| RsaPrivateKey::from_pkcs1_pem(&pem))
        .map_err(|_| AppError::Config("微信商户私钥格式无效".into()))?;
    if key.n().bits() != 2048 {
        return Err(AppError::Config(
            "微信支付需使用 RSA 2048 位商户私钥".into(),
        ));
    }
    Ok(key)
}
fn public_key(cfg: &WechatConfig) -> AppResult<RsaPublicKey> {
    let pem = std::fs::read_to_string(required(
        &cfg.platform_public_key_path,
        "WECHAT_PLATFORM_PUBLIC_KEY_PATH",
    )?)
    .map_err(|_| AppError::Config("无法读取微信支付平台公钥".into()))?;
    let key = RsaPublicKey::from_public_key_pem(&pem)
        .map_err(|_| AppError::Config("微信支付平台公钥需为 PEM PUBLIC KEY 格式".into()))?;
    if key.n().bits() != 2048 {
        return Err(AppError::Config("微信支付平台公钥长度无效".into()));
    }
    Ok(key)
}
pub fn validate(cfg: &WechatConfig) -> AppResult<()> {
    if cfg.pay_key.as_bytes().len() != 32 {
        return Err(AppError::Config("微信 APIv3 密钥必须为 32 字节".into()));
    }
    let serial = required(&cfg.merchant_serial_no, "WECHAT_MERCHANT_SERIAL_NO")?;
    if serial.len() > 64
        || !serial.bytes().all(|c| c.is_ascii_hexdigit())
        || cfg.mch_id.is_empty()
        || cfg.mch_id.len() > 32
        || !cfg.mch_id.bytes().all(|c| c.is_ascii_digit())
    {
        return Err(AppError::Config("微信商户号或证书序列号格式无效".into()));
    }
    let key_id = required(&cfg.platform_key_id, "WECHAT_PLATFORM_KEY_ID")?;
    if key_id.len() > 128
        || !key_id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'_')
    {
        return Err(AppError::Config("微信支付公钥 ID 格式无效".into()));
    }
    private_key(cfg)?;
    public_key(cfg)?;
    Ok(())
}
pub fn sign(cfg: &WechatConfig, message: &str) -> AppResult<String> {
    let key = private_key(cfg)?;
    let signature = key
        .sign_with_rng(
            &mut rand::rngs::OsRng,
            Pkcs1v15Sign::new::<Sha256>(),
            &Sha256::digest(message.as_bytes()),
        )
        .map_err(|_| AppError::Config("微信支付 RSA 签名失败".into()))?;
    Ok(STANDARD.encode(signature))
}
pub fn authorization(
    cfg: &WechatConfig,
    method: &str,
    url: &reqwest::Url,
    body: &str,
    timestamp: &str,
    nonce: &str,
) -> AppResult<String> {
    let target = match url.query() {
        Some(q) => format!("{}?{q}", url.path()),
        None => url.path().into(),
    };
    let signature = sign(
        cfg,
        &format!("{method}\n{target}\n{timestamp}\n{nonce}\n{body}\n"),
    )?;
    Ok(format!("WECHATPAY2-SHA256-RSA2048 mchid=\"{}\",nonce_str=\"{nonce}\",timestamp=\"{timestamp}\",serial_no=\"{}\",signature=\"{signature}\"",cfg.mch_id,required(&cfg.merchant_serial_no,"WECHAT_MERCHANT_SERIAL_NO")?))
}
pub fn verify_response(
    cfg: &WechatConfig,
    headers: &reqwest::header::HeaderMap,
    body: &str,
) -> AppResult<()> {
    let field = |name: &str| -> AppResult<&str> {
        headers
            .get(name)
            .and_then(|h| h.to_str().ok())
            .filter(|s| !s.is_empty())
            .ok_or_else(|| AppError::WechatPayFailed("微信支付响应缺少验签信息".into()))
    };
    let serial = field("wechatpay-serial")?;
    let timestamp = field("wechatpay-timestamp")?;
    let nonce = field("wechatpay-nonce")?;
    let signature = field("wechatpay-signature")?;
    if serial != required(&cfg.platform_key_id, "WECHAT_PLATFORM_KEY_ID")? {
        return Err(AppError::WechatPayFailed(
            "微信支付响应公钥 ID 不匹配".into(),
        ));
    }
    let ts = timestamp
        .parse::<i64>()
        .map_err(|_| AppError::WechatPayFailed("微信支付响应时间戳无效".into()))?;
    if chrono::Utc::now().timestamp().abs_diff(ts) > 300 {
        return Err(AppError::WechatPayFailed("微信支付响应时间戳已过期".into()));
    }
    let signature = STANDARD
        .decode(signature)
        .map_err(|_| AppError::WechatPayFailed("微信支付响应签名无效".into()))?;
    public_key(cfg)?
        .verify(
            Pkcs1v15Sign::new::<Sha256>(),
            &Sha256::digest(format!("{timestamp}\n{nonce}\n{body}\n").as_bytes()),
            &signature,
        )
        .map_err(|_| AppError::WechatPayFailed("微信支付响应验签失败".into()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rsa::pkcs8::{EncodePrivateKey, EncodePublicKey, LineEnding};
    use std::sync::OnceLock;
    static KEY: OnceLock<RsaPrivateKey> = OnceLock::new();
    struct Fixture {
        cfg: WechatConfig,
        private: std::path::PathBuf,
        public: std::path::PathBuf,
    }
    impl Fixture {
        fn new() -> Self {
            let key = KEY.get_or_init(|| RsaPrivateKey::new(&mut rand::rngs::OsRng, 2048).unwrap());
            let tag = uuid::Uuid::new_v4();
            let private = std::env::temp_dir().join(format!("wechat-test-{tag}-private.pem"));
            let public = std::env::temp_dir().join(format!("wechat-test-{tag}-public.pem"));
            std::fs::write(
                &private,
                key.to_pkcs8_pem(LineEnding::LF).unwrap().as_bytes(),
            )
            .unwrap();
            std::fs::write(
                &public,
                key.to_public_key()
                    .to_public_key_pem(LineEnding::LF)
                    .unwrap(),
            )
            .unwrap();
            Self {
                cfg: WechatConfig {
                    appid: "wx_test_app".into(),
                    secret: "test".into(),
                    mch_id: "1900000109".into(),
                    pay_key: "12345678901234567890123456789012".into(),
                    notify_url: "https://example.test/notify".into(),
                    pay_base_url: "https://api.mch.weixin.qq.com".into(),
                    refund_url: "https://api.mch.weixin.qq.com/v3/refund/domestic/refunds".into(),
                    refund_notify_url:None,
                    cert_path: None,
                    private_key_path: Some(private.to_string_lossy().into()),
                    merchant_serial_no: Some("AB12".into()),
                    platform_public_key_path: Some(public.to_string_lossy().into()),
                    platform_key_id: Some("PUB_KEY_ID_TEST".into()),
                },
                private,
                public,
            }
        }
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(&self.private);
            let _ = std::fs::remove_file(&self.public);
        }
    }
    #[test]
    fn payment_callback_verifies_raw_body_then_decrypts_and_validates_identity() {
        use ring::aead::{Aad, LessSafeKey, Nonce, UnboundKey, AES_256_GCM};
        let mut f = Fixture::new();
        f.cfg.pay_key = "12345678901234567890123456789012".into();
        let notification = serde_json::json!({"appid":f.cfg.appid,"mchid":f.cfg.mch_id,"out_trade_no":"PAY_TEST","transaction_id":"WX_TEST","trade_type":"JSAPI","trade_state":"SUCCESS","success_time":chrono::Utc::now().to_rfc3339(),"payer":{"openid":"user_test"},"amount":{"total":100,"payer_total":90,"currency":"CNY","payer_currency":"CNY"}});
        let encode = |value: &serde_json::Value| {
            let mut plain = serde_json::to_vec(value).unwrap();
            LessSafeKey::new(UnboundKey::new(&AES_256_GCM, f.cfg.pay_key.as_bytes()).unwrap())
                .seal_in_place_append_tag(
                    Nonce::try_assume_unique_for_key(b"123456789012").unwrap(),
                    Aad::from(b"transaction"),
                    &mut plain,
                )
                .unwrap();
            serde_json::to_string_pretty(&serde_json::json!({"event_type":"TRANSACTION.SUCCESS","resource_type":"encrypt-resource","resource":{"original_type":"transaction","algorithm":"AEAD_AES_256_GCM","nonce":"123456789012","associated_data":"transaction","ciphertext":STANDARD.encode(plain)}})).unwrap()
        };
        let signed = |body: &str| {
            let timestamp = chrono::Utc::now().timestamp().to_string();
            let mut h = reqwest::header::HeaderMap::new();
            h.insert("wechatpay-timestamp", timestamp.parse().unwrap());
            h.insert("wechatpay-nonce", "callback_nonce".parse().unwrap());
            h.insert("wechatpay-serial", "PUB_KEY_ID_TEST".parse().unwrap());
            h.insert(
                "wechatpay-signature",
                sign(&f.cfg, &format!("{timestamp}\ncallback_nonce\n{body}\n"))
                    .unwrap()
                    .parse()
                    .unwrap(),
            );
            h
        };
        let body = encode(&notification);
        let headers = signed(&body);
        assert_eq!(
            crate::decode_payment_notification(&f.cfg, &headers, body.as_bytes())
                .unwrap()
                .amount
                .total,
            100
        );
        assert!(matches!(
            crate::decode_payment_notification(&f.cfg, &headers, format!("{body} ").as_bytes()),
            Err(AppError::Unauthorized(_))
        ));
        let mut wrong = notification.clone();
        wrong["mchid"] = "another_merchant".into();
        let body = encode(&wrong);
        assert!(matches!(
            crate::decode_payment_notification(&f.cfg, &signed(&body), body.as_bytes()),
            Err(AppError::BadRequest(_))
        ));
        let mut wrong = notification.clone();
        wrong["amount"]["payer_total"] = 101.into();
        let body = encode(&wrong);
        assert!(
            crate::decode_payment_notification(&f.cfg, &signed(&body), body.as_bytes()).is_err()
        );
        let mut wrong = notification.clone();
        wrong["trade_state"] = "CLOSED".into();
        let body = encode(&wrong);
        assert!(
            crate::decode_payment_notification(&f.cfg, &signed(&body), body.as_bytes()).is_err()
        );
    }
    #[test]
    fn request_signs_exact_method_target_and_json_bytes() {
        let f = Fixture::new();
        validate(&f.cfg).unwrap();
        let url = reqwest::Url::parse(
            "https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi?test=%E4%B8%AD",
        )
        .unwrap();
        let body = "{\"description\":\"充电\",\"amount\":{\"total\":123}}";
        let auth = authorization(&f.cfg, "POST", &url, body, "1700000000", "nonce").unwrap();
        assert!(auth.contains("serial_no=\"AB12\""));
        let signature = STANDARD
            .decode(
                auth.split("signature=\"")
                    .nth(1)
                    .unwrap()
                    .trim_end_matches('"'),
            )
            .unwrap();
        let message =
            format!("POST\n/v3/pay/transactions/jsapi?test=%E4%B8%AD\n1700000000\nnonce\n{body}\n");
        public_key(&f.cfg)
            .unwrap()
            .verify(
                Pkcs1v15Sign::new::<Sha256>(),
                &Sha256::digest(message),
                &signature,
            )
            .unwrap();
    }
    #[test]
    fn refund_callback_binds_encrypted_status_merchant_and_amounts(){
        use ring::aead::{Aad,LessSafeKey,Nonce,UnboundKey,AES_256_GCM};
        let f=Fixture::new();
        let notice=serde_json::json!({"mchid":f.cfg.mch_id,"out_trade_no":"PAY_test","transaction_id":"420001","out_refund_no":"REF_test","refund_id":"500001","refund_status":"SUCCESS","success_time":chrono::Utc::now().to_rfc3339(),"amount":{"total":100,"refund":82,"payer_total":90,"payer_refund":74}});
        let encoded=|n:&serde_json::Value,event:&str|{
            let mut plain=serde_json::to_vec(n).unwrap();
            LessSafeKey::new(UnboundKey::new(&AES_256_GCM,f.cfg.pay_key.as_bytes()).unwrap()).seal_in_place_append_tag(Nonce::try_assume_unique_for_key(b"123456789012").unwrap(),Aad::from(b"refund"),&mut plain).unwrap();
            let raw=serde_json::json!({"event_type":event,"resource_type":"encrypt-resource","resource":{"original_type":"refund","algorithm":"AEAD_AES_256_GCM","nonce":"123456789012","associated_data":"refund","ciphertext":STANDARD.encode(plain)}}).to_string();
            let timestamp=chrono::Utc::now().timestamp().to_string();let mut headers=reqwest::header::HeaderMap::new();
            headers.insert("wechatpay-timestamp",timestamp.parse().unwrap());headers.insert("wechatpay-nonce","nonce".parse().unwrap());headers.insert("wechatpay-serial","PUB_KEY_ID_TEST".parse().unwrap());headers.insert("wechatpay-signature",sign(&f.cfg,&format!("{timestamp}\nnonce\n{raw}\n")).unwrap().parse().unwrap());(headers,raw)
        };
        let (h,raw)=encoded(&notice,"REFUND.SUCCESS");
        assert_eq!(crate::decode_refund_notification(&f.cfg,&h,raw.as_bytes()).unwrap().amount.refund,82);
        assert!(crate::decode_refund_notification(&f.cfg,&h,format!("{raw} ").as_bytes()).is_err());
        for change in ["merchant","amount","time","status"] {
            let mut bad=notice.clone();match change {"merchant"=>bad["mchid"]="other".into(),"amount"=>bad["amount"]["refund"]=101.into(),"time"=>bad["success_time"]=serde_json::Value::Null,_=>bad["refund_status"]="CLOSED".into()};
            let (h,raw)=encoded(&bad,"REFUND.SUCCESS");assert!(crate::decode_refund_notification(&f.cfg,&h,raw.as_bytes()).is_err());
        }
    }
    #[tokio::test]
    async fn refund_http_signs_and_verifies_response_before_acceptance() {
        use std::io::{Read,Write};
        let mut f=Fixture::new();
        let request=crate::RefundReq{transaction_id:Some("42000001".into()),out_trade_no:None,out_refund_no:"REF_test".into(),reason:None,notify_url:None,funds_account:None,amount:crate::RefundAmount{refund:82,total:100,currency:"CNY".into()}};
        for tamper in [false,true] {
            let listener=std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            f.cfg.refund_url=format!("http://{}/v3/refund/domestic/refunds",listener.local_addr().unwrap());
            let raw=serde_json::json!({"refund_id":"5000001","out_refund_no":"REF_test","transaction_id":"42000001","out_trade_no":"PAY_test","status":"PROCESSING","amount":{"refund":82,"total":100,"currency":"CNY"}}).to_string();
            let timestamp=chrono::Utc::now().timestamp().to_string();
            let sig=sign(&f.cfg,&format!("{timestamp}\nnonce\n{raw}\n")).unwrap();
            let sent=if tamper{format!("{raw} ")}else{raw};
            let server=std::thread::spawn(move || {
                let (mut socket,_)=listener.accept().unwrap();socket.set_read_timeout(Some(std::time::Duration::from_secs(10))).unwrap();
                let mut bytes=Vec::new();let mut b=[0u8;1];
                while !bytes.ends_with(b"\r\n\r\n"){socket.read_exact(&mut b).unwrap();bytes.push(b[0]);assert!(bytes.len()<16384);}
                let headers=String::from_utf8(bytes).unwrap();
                let size:usize=headers.lines().find_map(|line|line.to_ascii_lowercase().strip_prefix("content-length: ").map(str::to_owned)).unwrap().parse().unwrap();
                let mut body=vec![0;size];socket.read_exact(&mut body).unwrap();
                write!(socket,"HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: application/json\r\nWechatpay-Timestamp: {timestamp}\r\nWechatpay-Nonce: nonce\r\nWechatpay-Serial: PUB_KEY_ID_TEST\r\nWechatpay-Signature: {sig}\r\nConnection: close\r\n\r\n{sent}",sent.len()).unwrap();
                (headers,String::from_utf8(body).unwrap())
            });
            let result=crate::refund_once(&reqwest::Client::new(),&f.cfg,&request).await;
            assert_eq!(result.is_err(),tamper);
            let (headers,body)=server.join().unwrap();
            assert!(headers.starts_with("POST /v3/refund/domestic/refunds HTTP/1.1"));
            assert_eq!(body,serde_json::to_string(&request).unwrap());
            let auth=headers.lines().find(|v|v.to_ascii_lowercase().starts_with("authorization:")).unwrap();
            let field=|name:&str|auth.split(&format!("{name}=\"")).nth(1).unwrap().split('"').next().unwrap().to_string();
            let message=format!("POST\n/v3/refund/domestic/refunds\n{}\n{}\n{body}\n",field("timestamp"),field("nonce_str"));
            public_key(&f.cfg).unwrap().verify(Pkcs1v15Sign::new::<Sha256>(),&Sha256::digest(message),&STANDARD.decode(field("signature")).unwrap()).unwrap();
        }
    }
    #[test]
    fn frontend_signature_uses_appid_timestamp_nonce_and_package() {
        let f = Fixture::new();
        let pay = crate::sign_jsapi_pay(&f.cfg, "wx_prepay_test").unwrap();
        let message = format!(
            "{}\n{}\n{}\n{}\n",
            pay.appId, pay.timeStamp, pay.nonceStr, pay.package
        );
        public_key(&f.cfg)
            .unwrap()
            .verify(
                Pkcs1v15Sign::new::<Sha256>(),
                &Sha256::digest(message),
                &STANDARD.decode(pay.paySign).unwrap(),
            )
            .unwrap();
        assert_eq!(pay.signType, "RSA");
        assert_eq!(pay.package, "prepay_id=wx_prepay_test");
        assert!(crate::sign_jsapi_pay(&f.cfg, "bad\nprepay").is_err());
    }
    #[test]
    fn response_rejects_mutation_expiry_wrong_key_id_and_missing_headers() {
        let f = Fixture::new();
        let timestamp = chrono::Utc::now().timestamp().to_string();
        let body = "{ \"prepay_id\": \"abc\" }";
        let mut headers = reqwest::header::HeaderMap::new();
        for (key, value) in [
            ("wechatpay-serial", "PUB_KEY_ID_TEST".to_string()),
            ("wechatpay-timestamp", timestamp.clone()),
            ("wechatpay-nonce", "nonce".into()),
            (
                "wechatpay-signature",
                sign(&f.cfg, &format!("{timestamp}\nnonce\n{body}\n")).unwrap(),
            ),
        ] {
            headers.insert(
                reqwest::header::HeaderName::from_bytes(key.as_bytes()).unwrap(),
                value.parse().unwrap(),
            );
        }
        verify_response(&f.cfg, &headers, body).unwrap();
        assert!(verify_response(&f.cfg, &headers, "{\"prepay_id\":\"abc\"}").is_err());
        headers.insert("wechatpay-serial", "OTHER".parse().unwrap());
        assert!(verify_response(&f.cfg, &headers, body).is_err());
        headers.insert("wechatpay-serial", "PUB_KEY_ID_TEST".parse().unwrap());
        headers.insert("wechatpay-timestamp", "1".parse().unwrap());
        assert!(verify_response(&f.cfg, &headers, body).is_err());
        assert!(verify_response(&f.cfg, &reqwest::header::HeaderMap::new(), body).is_err());
    }
    #[test]
    fn missing_or_invalid_private_key_is_not_a_placeholder_signature() {
        let mut f = Fixture::new();
        f.cfg.private_key_path = None;
        assert!(validate(&f.cfg).is_err());
        assert!(crate::sign_jsapi_pay(&f.cfg, "abc").is_err());
    }
}
