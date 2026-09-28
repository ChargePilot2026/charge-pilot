//! Explicit development-only seed, invoked by the Docker development runner.
//! Existing credentials are preserved on subsequent container / watcher restarts.
//!
//! seed 脚本，SQL 属于脚本本身职责 —— 它一次性地铺角色、权限与首个管理员，
//! 不属于任何能力域，因此不适用 `disallowed-methods`（SQL 只能在
//! `repository_sql.rs`）那条约束。
#![allow(clippy::disallowed_methods)]

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
    sqlx::query("INSERT IGNORE INTO permission (code,name,module) VALUES ('device.import','导入设备','device'),('device.read','查看设备','device')")
        .execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO role_permission (role_id,permission_id) SELECT ?,id FROM permission WHERE code IN ('device.import','device.read')")
        .bind(role_id).execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO permission (code,name,module) VALUES ('station.read','查看站点','station'),('station.create','新增站点','station'),('station.update','编辑站点','station'),('station.delete','删除站点','station')")
        .execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO role_permission (role_id,permission_id) SELECT ?,id FROM permission WHERE code IN ('station.read','station.create','station.update','station.delete')")
        .bind(role_id).execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO permission (code,name,module) VALUES ('invoice.review','审核发票申请','finance'),('feedback.read','查看用户反馈','customer_service'),('feedback.reply','回复用户反馈','customer_service'),('fault.read','查看设备报修','inspection'),('fault.dispatch','派单处理设备报修','inspection'),('whitelabel.read','查看白标配置','settings'),('whitelabel.update','更新白标配置','settings'),('dashboard.read','查看运营仪表盘','dashboard')")
        .execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO role_permission (role_id,permission_id) SELECT ?,id FROM permission WHERE code IN ('invoice.review','feedback.read','feedback.reply','fault.read','fault.dispatch','whitelabel.read','whitelabel.update','dashboard.read')")
        .bind(role_id).execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO permission (code,name,module,description) VALUES ('coupon.read','查看优惠券','coupon','查询优惠券模板和发放统计'),('coupon.create','创建优惠券','coupon','创建优惠券模板'),('coupon.update','编辑优惠券','coupon','修改优惠券模板'),('coupon.delete','删除优惠券','coupon','软删除优惠券模板'),('coupon.grant','发放优惠券','coupon','向指定用户发放优惠券')")
        .execute(&mut *tx).await?;
    sqlx::query("INSERT IGNORE INTO role_permission (role_id,permission_id) SELECT ?,id FROM permission WHERE code IN ('coupon.read','coupon.create','coupon.update','coupon.delete','coupon.grant')")
        .bind(role_id).execute(&mut *tx).await?;
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
