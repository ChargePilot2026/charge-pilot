//! Explicit development-only seed, invoked by the Docker development runner.
//! Existing credentials are preserved on subsequent container / watcher restarts.
use common_auth::hash_password;
use common_error::{AppError, AppResult};
use sqlx::mysql::MySqlPoolOptions;

#[tokio::main]
async fn main() -> AppResult<()> {
    if std::env::var("RUNTIME_ENV").as_deref() != Ok("dev") {
        return Err(AppError::Config(
            "seed_dev_admin requires RUNTIME_ENV=dev".into(),
        ));
    }
    let required = |name: &str| {
        std::env::var(name)
            .ok()
            .filter(|value| !value.is_empty())
            .ok_or_else(|| AppError::Config(format!("{name} must be set")))
    };
    let pool = MySqlPoolOptions::new()
        .max_connections(1)
        .connect(&required("DATABASE_URL")?)
        .await?;
    let username = required("ADMIN_BOOTSTRAP_USER")?;
    let password = required("ADMIN_BOOTSTRAP_PASSWORD")?;
    let mut tx = pool.begin().await?;
    let existing: Option<u64> = sqlx::query_scalar(
        "SELECT id FROM admin_user_role WHERE username = ? AND deleted_at IS NULL LIMIT 1",
    )
    .bind(&username)
    .fetch_optional(&mut *tx)
    .await?;
    let role: Option<u64> = sqlx::query_scalar(
        "SELECT id FROM role WHERE code = 'dev_admin' AND deleted_at IS NULL LIMIT 1",
    )
    .fetch_optional(&mut *tx)
    .await?;
    let role_id = match role {
        Some(id) => id,
        None => sqlx::query(
            "INSERT INTO role (code, name, is_builtin) VALUES ('dev_admin', 'Development admin', 1)",
        )
        .execute(&mut *tx)
        .await?
        .last_insert_id(),
    };
    sqlx::query(
        "INSERT INTO permission (code, name, module)
         SELECT 'order.read', '查看订单', 'order'
         WHERE NOT EXISTS (SELECT 1 FROM permission WHERE code = 'order.read')",
    ).execute(&mut *tx).await?;
    sqlx::query(
        "INSERT IGNORE INTO role_permission (role_id, permission_id)
         SELECT ?, id FROM permission WHERE code = 'order.read'",
    ).bind(role_id).execute(&mut *tx).await?;
    if existing.is_some() {
        tx.commit().await?;
        println!("Development administrator already exists; credentials preserved.");
        return Ok(());
    }
    sqlx::query(
        "INSERT INTO admin_user_role (username, display_name, password_hash, role_id, status)
         VALUES (?, 'Development admin', ?, ?, 'active')",
    )
    .bind(&username)
    .bind(hash_password(&password)?)
    .bind(role_id)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    println!("Created development administrator: {username}");
    Ok(())
}
