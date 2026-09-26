use crate::AppState;
use axum::{extract::{Query,State},Json};
use common_auth::UserClaims;
use common_error::{ApiEnvelope,AppError,AppResult};
use serde::{Deserialize,Serialize};
use sqlx::Row;

#[derive(Serialize)]
pub struct Balance { balance_cents:i64, available_cents:i64, frozen_cents:i64, status:String }
pub async fn balance(State(st):State<AppState>,claims:UserClaims)->AppResult<Json<ApiEnvelope<Balance>>> {
    let rows:Vec<(i64,i64,String)>=sqlx::query_as("SELECT balance_cents,frozen_cents,status FROM wallet_account WHERE user_id=? AND deleted_at IS NULL")
        .bind(claims.user_id).fetch_all(st.db.pool()).await?;
    if rows.len()>1 {return Err(AppError::Conflict("钱包账户重复，请联系客服".into()));}
    let (available_cents,frozen_cents,status)=rows.into_iter().next().unwrap_or((0,0,"active".into()));
    Ok(Json(ApiEnvelope::ok(Balance {balance_cents:available_cents,available_cents,frozen_cents,status},common_error::current_request_id())))
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TxnQuery {page:Option<u32>,page_size:Option<u32>,#[serde(rename="type")]kind:Option<String>}
#[derive(Serialize)]
pub struct Txn {txn_no:String,txn_type:String,direction:String,amount_cents:i64,balance_after_cents:i64,remark:Option<String>,created_at:String}
#[derive(Serialize)]
pub struct TxnPage {page:u32,page_size:u32,total:i64,items:Vec<Txn>}
pub async fn txns(State(st):State<AppState>,claims:UserClaims,Query(q):Query<TxnQuery>)->AppResult<Json<ApiEnvelope<TxnPage>>> {
    let page=q.page.unwrap_or(1);let page_size=q.page_size.unwrap_or(20);
    if page==0 || !(1..=100).contains(&page_size) {return Err(AppError::BadRequest("分页参数无效".into()));}
    let kind=match q.kind.as_deref() {
        None|Some("")=>None,Some("consume")=>Some("pay"),Some("admin_adjust")=>Some("adjust"),
        Some(v @ ("recharge"|"refund"|"freeze"|"unfreeze"|"gift"))=>Some(v),
        _=>return Err(AppError::BadRequest("流水类型无效".into())),
    };
    let mut tx=st.db.pool().begin().await?;
    let total=sqlx::query_scalar("SELECT COUNT(*) FROM wallet_txn WHERE user_id=? AND (? IS NULL OR biz_type=?)")
        .bind(claims.user_id).bind(kind).bind(kind).fetch_one(&mut *tx).await?;
    let rows=sqlx::query("SELECT txn_no,biz_type,direction,amount_cents,balance_after_cents,note,created_at FROM wallet_txn WHERE user_id=? AND (? IS NULL OR biz_type=?) ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?")
        .bind(claims.user_id).bind(kind).bind(kind).bind(page_size).bind(u64::from(page-1)*u64::from(page_size)).fetch_all(&mut *tx).await?;
    let mut items=Vec::with_capacity(rows.len());
    for row in rows {
        let kind:String=row.try_get("biz_type")?;
        let direction:String=row.try_get("direction")?;
        let amount:i64=row.try_get("amount_cents")?;
        if amount<0 {return Err(AppError::Internal("钱包流水金额格式异常".into()));}
        let at:chrono::DateTime<chrono::Utc>=row.try_get("created_at")?;
        items.push(Txn {txn_no:row.try_get("txn_no")?,txn_type:match kind.as_str(){"pay"=>"consume".into(),"adjust"=>"admin_adjust".into(),_=>kind},
            amount_cents:if direction=="out" {-amount}else{amount},direction,balance_after_cents:row.try_get("balance_after_cents")?,
            remark:row.try_get("note")?,created_at:at.to_rfc3339()});
    }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(TxnPage {page,page_size,total,items},common_error::current_request_id())))
}
