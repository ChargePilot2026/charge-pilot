use common_error::{AppError, AppResult};
use sqlx::{MySql, Transaction};

pub async fn resolve_user(tx: &mut Transaction<'_, MySql>, openid: &str, unionid: Option<&str>) -> AppResult<(u64, bool)> {
    if openid.is_empty() || openid.len() > 64 || unionid.is_some_and(|v| v.is_empty() || v.len() > 64) {
        return Err(AppError::ServiceUnavailable("微信返回的用户标识无效".into()));
    }
    sqlx::query("INSERT INTO user_login_identity (openid) VALUES (?) ON DUPLICATE KEY UPDATE openid=VALUES(openid)")
        .bind(openid.as_bytes()).execute(&mut **tx).await?;
    let rows: Vec<(u64, String, bool, Option<String>)> = sqlx::query_as(
        "SELECT id,status,(deleted_at IS NOT NULL),unionid FROM `user` WHERE BINARY openid=? FOR UPDATE"
    ).bind(openid.as_bytes()).fetch_all(&mut **tx).await?;
    if rows.len() > 1 { return Err(AppError::Conflict("账户标识重复，请联系客服处理".into())); }
    if let Some((id, status, deleted, existing_unionid)) = rows.into_iter().next() {
        if deleted || status != "active" { return Err(AppError::Forbidden("账户已停用".into())); }
        if let (Some(old), Some(new)) = (existing_unionid.as_deref(), unionid) {
            if old != new { return Err(AppError::Conflict("微信账户关联不一致".into())); }
        }
        sqlx::query("UPDATE `user` SET last_login_at=UTC_TIMESTAMP(3),unionid=COALESCE(unionid,?) WHERE id=?")
            .bind(unionid).bind(id).execute(&mut **tx).await?;
        Ok((id, false))
    } else {
        let id = sqlx::query("INSERT INTO `user` (openid,unionid,last_login_at) VALUES (?,?,UTC_TIMESTAMP(3))")
            .bind(openid).bind(unionid).execute(&mut **tx).await?.last_insert_id();
        Ok((id, true))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires migrated development user database"]
    async fn concurrent_login_has_one_identity_and_disabled_accounts_stay_disabled() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap()).await.unwrap();
        let openid = format!("login_test_{}",uuid::Uuid::new_v4().simple());
        let login = || async {
            let mut tx = pool.begin().await.unwrap();
            let result = resolve_user(&mut tx,&openid,Some("test-union")).await;
            if result.is_ok() { tx.commit().await.unwrap(); }
            result
        };
        let (first, second) = tokio::join!(login(),login());
        // Clean up even if a following assertion fails.
        let test_pool=pool.clone();
        let test_openid=openid.clone();
        let outcome = tokio::spawn(async move {
            let pool=test_pool; let openid=test_openid;
            let first = first.unwrap(); let second = second.unwrap();
            assert_eq!(first.0,second.0); assert_ne!(first.1,second.1);
            let mut tx = pool.begin().await.unwrap();
            assert_eq!(resolve_user(&mut tx,&openid,None).await.unwrap(),(first.0,false));
            sqlx::query("UPDATE `user` SET status='frozen' WHERE id=?").bind(first.0).execute(&mut *tx).await.unwrap();
            assert!(matches!(resolve_user(&mut tx,&openid,None).await,Err(AppError::Forbidden(_))));
            sqlx::query("UPDATE `user` SET status='active',deleted_at=UTC_TIMESTAMP() WHERE id=?").bind(first.0).execute(&mut *tx).await.unwrap();
            assert!(matches!(resolve_user(&mut tx,&openid,None).await,Err(AppError::Forbidden(_))));
            tx.rollback().await.unwrap();
        }).await;
        sqlx::query("DELETE FROM `user` WHERE openid=?").bind(&openid).execute(&pool).await.unwrap();
        sqlx::query("DELETE FROM user_login_identity WHERE openid=?").bind(openid.as_bytes()).execute(&pool).await.unwrap();
        outcome.unwrap();
    }
}
