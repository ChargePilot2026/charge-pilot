//! Server-side quotes bind consent to user, port, estimates and tariff.
use api_contracts::pricing::PriceQuote;
use chrono::{DateTime,Utc};
use common_error::{AppError,AppResult};
use common_redis::RedisCache;
use serde::{Deserialize,Serialize};

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct SavedQuote {
    pub user_id:u64,
    pub port_id:String,
    pub expires_at:DateTime<Utc>,
    pub quote:PriceQuote,
}
#[derive(Serialize)]
pub struct Preview {
    pub quote_id:String,
    #[serde(flatten)]
    pub quote:PriceQuote,
}
pub fn canonical_id(id:&str)->AppResult<String>{
    let id=uuid::Uuid::parse_str(id).map_err(|_|AppError::BadRequest("报价标识无效，请重新预估费用".into()))?;
    Ok(id.to_string())
}
fn key(id:&str)->AppResult<String>{
    Ok(format!("charge:quote:{}",canonical_id(id)?))
}
pub async fn save(cache:&RedisCache,user_id:u64,port_id:String,quote:PriceQuote)->AppResult<Preview>{
    crate::checkout::validate_quote(&quote.amount)?;
    let expires=DateTime::parse_from_rfc3339(&quote.quote_expires_at)
        .map_err(|_|AppError::ServiceUnavailable("报价有效期无效".into()))?.with_timezone(&Utc);
    let expires_at=expires.min(Utc::now()+chrono::Duration::minutes(5));
    if expires_at<=Utc::now(){return Err(AppError::Conflict("报价已过期，请重新预估费用".into()));}
    let mut quote=quote;quote.quote_expires_at=expires_at.to_rfc3339();
    let id=uuid::Uuid::new_v4().to_string();
    cache.set_ex(&key(&id)?,&SavedQuote{user_id,port_id,expires_at,quote:quote.clone()},300).await?;
    Ok(Preview{quote_id:id,quote})
}
pub async fn load(cache:&RedisCache,id:&str,user_id:u64,port_id:&str)->AppResult<SavedQuote>{
    let saved:SavedQuote=cache.get(&key(id)?).await?.ok_or_else(||AppError::Conflict("报价已过期，请重新预估费用".into()))?;
    if saved.user_id!=user_id || saved.port_id!=port_id {return Err(AppError::NotFound("报价不存在".into()));}
    if saved.expires_at<=Utc::now(){return Err(AppError::Conflict("报价已过期，请重新预估费用".into()));}
    Ok(saved)
}
pub fn verify(saved:&SavedQuote,current:&PriceQuote)->AppResult<()> {
    crate::checkout::validate_quote(&current.amount)?;
    if saved.expires_at<=Utc::now()
        || serde_json::to_value(&saved.quote.pricing)?!=serde_json::to_value(&current.pricing)?
        || serde_json::to_value(&saved.quote.amount)?!=serde_json::to_value(&current.amount)?
        || saved.quote.estimated_kwh!=current.estimated_kwh
        || saved.quote.estimated_minutes!=current.estimated_minutes {
        return Err(AppError::Conflict("报价已变化或过期，请重新预估并确认费用".into()));
    }
    Ok(())
}
pub async fn persist(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,id:u64,quote_id:&str,saved:&SavedQuote)->AppResult<()> {
    if saved.expires_at<=Utc::now(){return Err(AppError::Conflict("报价已过期，请重新预估费用".into()));}
    let quote_id=canonical_id(quote_id)?;
    let result=sqlx::query("INSERT INTO charge_order_pricing (charge_order_id,quote_id,user_id,port_code,quote_snapshot) VALUES (?,?,?,?,?)")
        .bind(id).bind(quote_id).bind(saved.user_id).bind(&saved.port_id).bind(serde_json::to_value(&saved.quote)?).execute(&mut **tx).await;
    match result {
        Ok(_)=>Ok(()),
        Err(sqlx::Error::Database(e)) if e.is_unique_violation()=>Err(AppError::Conflict("报价已用于订单，请在订单列表继续处理".into())),
        Err(e)=>Err(e.into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn saved()->SavedQuote {
        let expires_at=Utc::now()+chrono::Duration::minutes(5);
        SavedQuote {user_id:123,port_id:"CONFIRM_TEST:1".into(),expires_at,
            quote:PriceQuote {amount:api_contracts::QuoteResponse {electric_cents:50,service_cents:20,total_cents:70},
                pricing:api_contracts::pricing::DevicePricing {station_id:1,station_name:"test".into(),rule_id:1,name:"test".into(),version:1,mode:"kwh".into(),time_of_use:serde_json::json!([]),service_fee_cents_per_kwh:40,service_fee_cents_per_min:0,min_charge_cents:0},
                estimated_kwh:"0.500".into(),estimated_minutes:120,quote_expires_at:expires_at.to_rfc3339(),estimation_basis:"test".into()}}
    }
    #[test]
    fn rejects_drift_even_when_total_is_unchanged() {
        let s=saved();verify(&s,&s.quote).unwrap();
        let mut changed=s.quote.clone();changed.pricing.version+=1;assert!(verify(&s,&changed).is_err());
        let mut changed=s.quote.clone();changed.pricing.service_fee_cents_per_kwh+=1;assert!(verify(&s,&changed).is_err());
        let mut changed=s.quote.clone();changed.amount.electric_cents=40;changed.amount.service_cents=30;assert!(verify(&s,&changed).is_err());
        let mut expired=s.clone();expired.expires_at=Utc::now()-chrono::Duration::seconds(1);assert!(verify(&expired,&s.quote).is_err());
    }
    #[tokio::test]
    #[ignore="requires development MySQL"]
    async fn snapshot_uniqueness_and_checkout_rollback() {
        let pool=sqlx::mysql::MySqlPoolOptions::new().connect(&std::env::var("DATABASE_URL").unwrap()).await.unwrap();
        let s=saved();let qid=uuid::Uuid::new_v4().to_string();let no=format!("confirm_{}",uuid::Uuid::new_v4().simple());
        let port=api_contracts::ScanPortDetail {port_id:s.port_id.clone(),port_code:s.port_id.clone(),device_id:"CONFIRM_TEST".into(),port_no:1,status:"idle".into()};
        let mut tx=pool.begin().await.unwrap();
        let id=crate::checkout::persist_pending(&mut tx,s.user_id,&no,&format!("{no}_pay"),&port,&s.quote.amount,s.expires_at).await.unwrap();
        persist(&mut tx,id,&qid,&s).await.unwrap();
        let snapshot:serde_json::Value=sqlx::query_scalar("SELECT quote_snapshot FROM charge_order_pricing WHERE charge_order_id=?").bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(snapshot["total_cents"],70);assert_eq!(snapshot["pricing"]["version"],1);
        assert!(matches!(persist(&mut tx,id+1,&qid.to_uppercase(),&s).await,Err(AppError::Conflict(_))));
        tx.rollback().await.unwrap();
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM charge_order WHERE order_no=?").bind(&no).fetch_one(&pool).await.unwrap();assert_eq!(count,0);
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM payment_order WHERE order_no=?").bind(format!("{no}_pay")).fetch_one(&pool).await.unwrap();assert_eq!(count,0);
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM charge_order_pricing WHERE quote_id=?").bind(qid).fetch_one(&pool).await.unwrap();assert_eq!(count,0);
    }
}
