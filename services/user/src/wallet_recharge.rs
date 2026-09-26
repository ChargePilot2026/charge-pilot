//! Durable wallet recharge checkout. Provider retries use the original payment identity.
use crate::AppState;
use common_error::{AppError,AppResult};
use serde_json::{json,Value};
use sqlx::Row;

pub async fn prepare(st:&AppState,uid:u64,openid:&str,req:&crate::wallet::RechargeReq)->AppResult<Value> {
 let id=uuid::Uuid::parse_str(&req.request_id).map_err(|_|AppError::BadRequest("request_id 必须为 UUID".into()))?.to_string();
 if id!=req.request_id || !(100..=100_000_000).contains(&req.amount_cents) {return Err(AppError::BadRequest("充值金额须在 1 元至 100 万元之间".into()));}
 let cfg=st.cfg.wechat.as_ref().ok_or_else(||AppError::Config("wechat missing".into()))?;
 common_wechat::validate_pay_config(cfg)?;
 let mut tx=st.db.pool().begin().await?;
 sqlx::query("INSERT IGNORE INTO wallet_recharge_request(request_id,user_id,amount_cents) VALUES(?,?,?)").bind(&id).bind(uid).bind(req.amount_cents).execute(&mut *tx).await?;
 let row=sqlx::query("SELECT user_id,amount_cents,payment_order_id FROM wallet_recharge_request WHERE request_id=? FOR UPDATE").bind(&id).fetch_one(&mut *tx).await?;
 if row.try_get::<u64,_>("user_id")?!=uid || row.try_get::<i64,_>("amount_cents")?!=req.amount_cents {return Err(AppError::Conflict("充值请求编号已用于其他请求".into()));}
 let pid=match row.try_get::<Option<u64>,_>("payment_order_id")? {
  Some(pid)=>pid,
  None=>{
   let no=common_db::IdGen::new("PAY").next();
   let pid=sqlx::query("INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,status,created_month,expired_at,client_ip) VALUES(?,'wallet_recharge',0,?,'wechat',?,'initiated',DATE_FORMAT(UTC_TIMESTAMP(),'%Y-%m-01'),DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 5 MINUTE),?)").bind(&no).bind(uid).bind(req.amount_cents).bind(req.client_ip.as_deref()).execute(&mut *tx).await?.last_insert_id();
   let expiry:chrono::NaiveDateTime=sqlx::query_scalar("SELECT expired_at FROM payment_order WHERE id=?").bind(pid).fetch_one(&mut *tx).await?;
   let request=common_wechat::JsapiOrderReq {appid:cfg.appid.clone(),mchid:cfg.mch_id.clone(),description:format!("钱包充值 {no}"),out_trade_no:no,time_expire:expiry.and_utc().to_rfc3339(),attach:Some(json!({"pay_order_id":pid,"type":"wallet_recharge"}).to_string()),notify_url:cfg.notify_url.clone(),amount:common_wechat::JsapiAmount{total:req.amount_cents as i32,currency:"CNY".into()},payer:common_wechat::JsapiPayer{openid:openid.into()}};
   sqlx::query("UPDATE wallet_recharge_request SET payment_order_id=?,request_json=? WHERE request_id=?").bind(pid).bind(serde_json::to_value(request)?).bind(&id).execute(&mut *tx).await?;
   pid
  }
 };
 // Commit identity before any provider call, including calls whose response may be lost.
 tx.commit().await?;
 let mut tx=st.db.pool().begin().await?;
 let pay=sqlx::query("SELECT order_no,status,expired_at,total_cents FROM payment_order WHERE id=? AND user_id=? AND biz_type='wallet_recharge' AND deleted_at IS NULL FOR UPDATE").bind(pid).bind(uid).fetch_one(&mut *tx).await?;
 let status:String=pay.try_get("status")?;
 let expiry:chrono::NaiveDateTime=pay.try_get("expired_at")?;
 let mut result=json!({"request_id":id,"pay_order_id":pid.to_string(),"pay_order_no":pay.try_get::<String,_>("order_no")?,"amount_cents":req.amount_cents,"status":status,"expires_at":expiry.and_utc().to_rfc3339()});
 if status!="initiated" || expiry.and_utc()<=chrono::Utc::now() {result["can_pay"]=json!(false);tx.commit().await?;return Ok(result);}
 let saved=sqlx::query("SELECT request_json,prepay_id FROM wallet_recharge_request WHERE request_id=? FOR UPDATE").bind(&id).fetch_one(&mut *tx).await?;
 let request:common_wechat::JsapiOrderReq=serde_json::from_value(saved.try_get("request_json")?)?;
 let request_expiry=chrono::DateTime::parse_from_rfc3339(&request.time_expire).map_err(|_|AppError::Conflict("支付到期时间异常".into()))?;
 if request.amount.currency!="CNY" || request_expiry.timestamp_millis()!=expiry.and_utc().timestamp_millis() {return Err(AppError::Conflict("充值支付快照不一致".into()));}
 if request.appid!=cfg.appid || request.mchid!=cfg.mch_id || request.payer.openid!=openid || request.out_trade_no!=pay.try_get::<String,_>("order_no")? || i64::from(request.amount.total)!=pay.try_get::<i64,_>("total_cents")? {return Err(AppError::Conflict("充值支付信息不一致，请联系客服".into()));}
 let prepay=match saved.try_get::<Option<String>,_>("prepay_id")? {Some(v)=>v,None=>{
  let response=common_wechat::jsapi_create_order(&st.http,cfg,&request).await?;
  sqlx::query("UPDATE wallet_recharge_request SET prepay_id=? WHERE request_id=?").bind(&response.prepay_id).bind(&id).execute(&mut *tx).await?;
  response.prepay_id
 }};
 let params=common_wechat::sign_jsapi_pay(cfg,&prepay)?;
 tx.commit().await?;
 let can_pay=expiry.and_utc()>chrono::Utc::now();
 result["can_pay"]=json!(can_pay);
 if can_pay {result["payment_params"]=serde_json::to_value(params)?;}
 Ok(result)
}

#[derive(serde::Deserialize)]
pub struct ListQuery {pub page:Option<u32>}
pub async fn list(axum::extract::State(st):axum::extract::State<AppState>,claims:common_auth::UserClaims,axum::extract::Query(q):axum::extract::Query<ListQuery>)->AppResult<axum::Json<common_error::ApiEnvelope<Value>>> {
 let page=q.page.unwrap_or(1);
 if page==0 || page>100000 {return Err(AppError::BadRequest("分页无效".into()));}
 let rows=sqlx::query("SELECT CAST(r.request_id AS CHAR CHARACTER SET utf8mb4) request_id,p.order_no,p.total_cents,p.status,p.expired_at FROM wallet_recharge_request r JOIN payment_order p ON p.id=r.payment_order_id AND p.user_id=r.user_id WHERE r.user_id=? AND p.deleted_at IS NULL ORDER BY r.created_at DESC,r.request_id DESC LIMIT 20 OFFSET ?").bind(claims.user_id).bind(u64::from(page-1)*20).fetch_all(st.db.pool()).await?;
 let mut items=vec![];
 for row in rows {let expiry:chrono::NaiveDateTime=row.try_get("expired_at")?;let status:String=row.try_get("status")?;
 items.push(json!({"request_id":row.try_get::<String,_>("request_id")?,"pay_order_no":row.try_get::<String,_>("order_no")?,"amount_cents":row.try_get::<i64,_>("total_cents")?,"can_pay":status=="initiated" && expiry.and_utc()>chrono::Utc::now(),"status":status,"expires_at":expiry.and_utc().to_rfc3339()}));}
 Ok(axum::Json(common_error::ApiEnvelope::ok(json!({"user_id":claims.user_id.to_string(),"page":page,"items":items}),common_error::current_request_id())))
}

#[cfg(test)]
mod tests {
 use super::*;
 use rsa::pkcs8::{EncodePrivateKey,EncodePublicKey,LineEnding};
 use std::sync::Arc;
 #[tokio::test]
 #[ignore="requires development MySQL and Redis"]
 async fn recharge_retries_preserve_identity_and_expiry() {
        let mut cfg = common_config::AppConfig::load().unwrap();
        let tag = format!("PP_{}", uuid::Uuid::new_v4().simple());
        let key = rsa::RsaPrivateKey::new(&mut rand::rngs::OsRng, 2048).unwrap();
        let private = std::env::temp_dir().join(format!("{tag}.key"));
        let public = std::env::temp_dir().join(format!("{tag}.pub"));
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
        let wechat = cfg.wechat.as_mut().unwrap();
        wechat.mch_id = "1900000109".into();
        wechat.merchant_serial_no = Some("AB12".into());
        wechat.platform_key_id = Some("PUB_KEY_ID_TEST".into());
        wechat.private_key_path = Some(private.to_string_lossy().into());
        wechat.platform_public_key_path = Some(public.to_string_lossy().into());
        wechat.pay_base_url = "http://127.0.0.1:1".into();
        let st = AppState {
            db: common_db::Db::connect(&cfg.mysql).await.unwrap(),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache)
                .await
                .unwrap(),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream)
                .await
                .unwrap(),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: Arc::new(cfg),
        };

 let mut req=crate::wallet::RechargeReq{request_id:uuid::Uuid::new_v4().to_string(),amount_cents:1001,client_ip:None};
 // Unreachable mock provider: persistence must survive both failed calls.
 assert!(prepare(&st,123,&tag,&req).await.is_err());
 let pid:u64=sqlx::query_scalar("SELECT payment_order_id FROM wallet_recharge_request WHERE request_id=?").bind(&req.request_id).fetch_one(st.db.pool()).await.unwrap();
 let before:(String,chrono::NaiveDateTime)=sqlx::query_as("SELECT order_no,expired_at FROM payment_order WHERE id=?").bind(pid).fetch_one(st.db.pool()).await.unwrap();
 assert!(prepare(&st,123,&tag,&req).await.is_err());
 let same:u64=sqlx::query_scalar("SELECT payment_order_id FROM wallet_recharge_request WHERE request_id=?").bind(&req.request_id).fetch_one(st.db.pool()).await.unwrap();assert_eq!(pid,same);
 sqlx::query("UPDATE wallet_recharge_request SET prepay_id='cached_recharge_test' WHERE request_id=?").bind(&req.request_id).execute(st.db.pool()).await.unwrap();
 let cached=prepare(&st,123,&tag,&req).await.unwrap();
 assert_eq!(cached["payment_params"]["package"],"prepay_id=cached_recharge_test");
 assert_eq!(cached["expires_at"],before.1.and_utc().to_rfc3339());
 assert!(prepare(&st,124,&tag,&req).await.is_err());assert!(prepare(&st,123,"other",&req).await.is_err());
 req.amount_cents=1002;assert!(prepare(&st,123,&tag,&req).await.is_err());req.amount_cents=1001;
 sqlx::query("UPDATE payment_order SET expired_at=DATE_SUB(UTC_TIMESTAMP(3),INTERVAL 1 SECOND) WHERE id=?").bind(pid).execute(st.db.pool()).await.unwrap();
 let expired=prepare(&st,123,&tag,&req).await.unwrap();assert_eq!(expired["can_pay"],false);assert!(expired.get("payment_params").is_none());
 sqlx::query("UPDATE payment_order SET status='paid' WHERE id=?").bind(pid).execute(st.db.pool()).await.unwrap();
 let paid=prepare(&st,123,&tag,&req).await.unwrap();assert_eq!(paid["status"],"paid");assert_eq!(paid["can_pay"],false);
 let claims=common_auth::UserClaims{sub:tag.clone(),user_id:123,sid:None,role:None,exp:0,iat:0,iss:"test".into()};
 let response=list(axum::extract::State(st.clone()),claims.clone(),axum::extract::Query(ListQuery{page:Some(1)})).await.unwrap();
 let value=serde_json::to_value(response.0).unwrap();assert!(value["data"]["items"].as_array().unwrap().iter().any(|v|v["request_id"]==req.request_id && v["status"]=="paid"));
 let mut other=claims.clone();other.user_id=124;
 let response=list(axum::extract::State(st.clone()),other,axum::extract::Query(ListQuery{page:Some(1)})).await.unwrap();
 let value=serde_json::to_value(response.0).unwrap();assert!(!value["data"]["items"].as_array().unwrap().iter().any(|v|v["request_id"]==req.request_id));
 assert!(list(axum::extract::State(st.clone()),claims,axum::extract::Query(ListQuery{page:Some(0)})).await.is_err());
 sqlx::query("DELETE FROM wallet_recharge_request WHERE request_id=?").bind(&req.request_id).execute(st.db.pool()).await.unwrap();
 sqlx::query("DELETE FROM payment_order WHERE id=?").bind(pid).execute(st.db.pool()).await.unwrap();
 std::fs::remove_file(private).unwrap();std::fs::remove_file(public).unwrap();
 }
}
