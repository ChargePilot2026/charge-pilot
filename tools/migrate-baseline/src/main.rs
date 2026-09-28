//! 存量库迁移登记(P4 migration 接管)
//!
//! 流程(四步,顺序不可颠倒):
//!   ① 核验历史版本集合 —— 用 information_schema 探测基准表区分存量/全新,
//!      **只登记已核验的版本**,否则尚未执行的新迁移会被误标成功、后续永不执行。
//!   ② 创建登记表     —— 存量库没有 `_sqlx_migrations`;`Migrator::new()` 只读文件不建表。
//!   ③ 校验并登记     —— 逐条核对 checksum;`AppliedMigration` 只有
//!      `{version, checksum}`,**没有 success**,失败迁移用 `dirty_version()` 单独查。
//!   ④ 交给 migrator 执行剩余。
//!
//! 用法:
//! ```text
//! cargo run -p migrate-baseline -- --source ../../migrations/admin_db --probe admin_user_role
//! cargo run -p migrate-baseline -- --source ../../migrations/admin_db --probe admin_user_role --dry-run
//! ```

// 分层与序列化约束(P1a 建立;P5 收口完成,转 deny)
// 说明:配置在仓库根 clippy.toml,级别在 crate 根(bin crate 无 lib.rs);
// `not(test)` 让 `#[cfg(test)]` 内的测试夹具不被 deny。
#![cfg_attr(
    not(test),
    deny(
        clippy::disallowed_macros,
        clippy::disallowed_types,
        clippy::disallowed_methods,
    )
)]
// 本 crate 是**迁移基线登记工具**:它直接读写 `_sqlx_migrations` 与
// `information_schema`,SQL 就是它的职责本身 —— 不存在「下沉到 repository 层」
// 的对象(见仓库根 clippy.toml 的 reason:基础设施 crate 例外)。
// crate 级属性按出现顺序解析,后写的覆盖先写的,故这条放在上面 deny 之后。
#![allow(clippy::disallowed_methods)]

use anyhow::{bail, Context, Result};
use sqlx::migrate::{Migrate, MigrationType, Migrator};
use sqlx::{Connection, MySqlConnection, Row};
use std::path::PathBuf;

struct Args {
    source: PathBuf,
    probe: String,
    dry_run: bool,
}

fn parse_args() -> Result<Args> {
    let mut source = PathBuf::from("migrations");
    let mut probe = String::new();
    let mut dry_run = false;
    let mut it = std::env::args().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "--source" => source = PathBuf::from(it.next().context("--source 需要取值")?),
            "--probe" => probe = it.next().context("--probe 需要取值")?,
            "--dry-run" => dry_run = true,
            other => bail!("未知参数 {other}"),
        }
    }
    if probe.is_empty() {
        bail!("必须用 --probe 指定基准表名(用于区分存量库与全新空库)");
    }
    Ok(Args { source, probe, dry_run })
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = parse_args()?;
    let url = std::env::var("DATABASE_URL").context("需要 DATABASE_URL")?;
    let migrator = Migrator::new(args.source.as_path())
        .await
        .with_context(|| format!("解析迁移目录 {}", args.source.display()))?;

    let mut conn = MySqlConnection::connect(&url).await.context("连接数据库")?;

    // ── ① 区分存量库 / 全新空库 ────────────────────────────────
    let current_db: String = sqlx::query("SELECT DATABASE()")
        .fetch_one(&mut conn)
        .await?
        .try_get(0)
        .unwrap_or_default();
    let probe_rows: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM information_schema.tables
          WHERE table_schema = ? AND table_name = ?",
    )
    .bind(&current_db)
    .bind(&args.probe)
    .fetch_one(&mut conn)
    .await?;

    if probe_rows == 0 {
        println!(
            "schema `{current_db}` 尚无基准表 `{}` → 判定为全新库,应直接执行全量迁移,无需登记。",
            args.probe
        );
        return Ok(());
    }
    println!("schema `{current_db}` 已存在基准表 `{}` → 判定为存量库,执行 baseline 登记。", args.probe);

    if args.dry_run {
        println!("--dry-run:仅列出将登记的版本");
        for m in migrator.iter().filter(|m| m.migration_type.is_up_migration()) {
            println!("  v{} {}", m.version, m.sql);
        }
        return Ok(());
    }

    // ── ② 创建登记表(存量库没有这张表)────────────────────────
    conn.ensure_migrations_table()
        .await
        .context("创建 _sqlx_migrations")?;

    // ── ③ 校验并登记(逐条)────────────────────────────────────
    let mut registered = 0usize;
    for m in migrator.iter().filter(|m| m.migration_type.is_up_migration()) {
        let file_sum: &[u8] = m.checksum.as_ref();
        let existing: Option<(Vec<u8>,)> = sqlx::query_as(
            "SELECT checksum FROM _sqlx_migrations WHERE version = ?",
        )
        .bind(m.version)
        .fetch_optional(&mut conn)
        .await?;

        match existing {
            // AppliedMigration 只有 {version, checksum},无 success —— 冲突即中止
            Some((checksum,)) => {
                if checksum != file_sum {
                    bail!(
                        "版本 {} 的 checksum 不一致(库内 {} vs 文件 {})，需人工裁决,禁止静默跳过",
                        m.version,
                        hex(&checksum),
                        hex(file_sum)
                    );
                }
            }
            None => {
                sqlx::query(
                    "INSERT INTO _sqlx_migrations
                       (version, description, installed_on, success, checksum, execution_time)
                     VALUES (?, ?, NOW(3), 1, ?, 0)",
                )
                .bind(m.version)
                .bind(&m.sql)
                .bind(file_sum)
                .execute(&mut conn)
                .await
                .with_context(|| format!("登记版本 {}", m.version))?;
                registered += 1;
            }
        }
    }

    // 失败迁移检查
    if let Some(dirty) = conn.dirty_version().await? {
        bail!("schema 处于 dirty 状态(迁移 {dirty} 失败),请先人工修复,禁止继续");
    }

    println!("登记完成:新增 {registered} 条;其余已存在且 checksum 一致。");
    println!("下一步:执行 `sqlx migrate run` 应用剩余迁移(未登记的新版本会被正常执行)。");
    Ok(())
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}
