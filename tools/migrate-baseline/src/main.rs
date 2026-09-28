//! 存量库迁移登记(P4 migration 接管)
//!
//! 流程(五步,顺序不可颠倒):
//!   ① 核验历史版本集合 —— 用 information_schema 探测基准表区分存量/全新,
//!      **只登记已核验的版本**,否则尚未执行的新迁移会被误标成功、后续永不执行。
//!   ② 创建登记表     —— 两条分支都要:全新库也没有 `_sqlx_migrations`,
//!      而 `dirty_version()` 直接 SELECT 它,表不存在时 MySQL 返 1146 而不是空结果集。
//!   ③ 校验并登记     —— 逐条核对 checksum **与物理对象是否真的存在**;
//!      `AppliedMigration` 只有 `{version, checksum}`,**没有 success**。
//!   ④ dirty 检查     —— 库里存在失败迁移时必须中止,禁止在脏状态上叠加新迁移。
//!   ⑤ 执行剩余       —— 两条分支最终都交给 `Migrator::run()`;全新库由此建出全量 schema。
//!
//! 用法:
//! ```text
//! cargo run -p migrate-baseline -- --source ../../migrations/admin_db --probe admin_user_role
//! cargo run -p migrate-baseline -- --source ../../migrations/admin_db --probe admin_user_role --dry-run
//! ```
//!
//! 设计依据(V9 的「只登记已核验版本」):
//! **登记表是唯一事实来源,写一行就等于断言「这段 DDL 已经生效」。**
//! 因此只对有物理证据的迁移登记 —— `CREATE TABLE` 的表存在,或 `ADD COLUMN` 的列存在;
//! 没证据的**不登记**,让 `Migrator::run()` 去真正执行它。
//! 本次真实踩到的坑: `0022_event_outbox` 首次执行失败留 dirty,修好 SQL 重跑时
//! 若无条件登记全部版本,就会把一个**从未建出来**的表标成 success=1,
//! `Migrator::run()` 随后认为它已应用而彻底跳过。症状是 admin 持续报
//! `Table 'admin_db.event_outbox' doesn't exist`,而登记表里 22 号版本明明写着 success。

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
// `Migrate` 提供 `Migrator::run` / `ensure_migrations_table` / `dirty_version`。
// `MigrationType::is_up_migration` 是固有方法,无需 import。
use sqlx::migrate::{Migrate, Migrator};
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
    // 同时看两个信号:_sqlx_migrations 存在即说明本工具(或历史上)已接管过这个库,
    // 此时即便基准表缺失(例如只建了一半)也不能按「全新库」处理。
    let probe_rows: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM information_schema.tables
          WHERE table_schema = ? AND table_name = ?",
    )
    .bind(&current_db)
    .bind(&args.probe)
    .fetch_one(&mut conn)
    .await?;
    let has_registry: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM information_schema.tables
          WHERE table_schema = ? AND table_name = '_sqlx_migrations'",
    )
    .bind(&current_db)
    .fetch_one(&mut conn)
    .await?;

    let fresh = probe_rows == 0 && has_registry == 0;
    if fresh {
        println!(
            "schema `{current_db}` 无基准表 `{}` 且无 `_sqlx_migrations` → 判定为全新库,直接执行全量迁移。",
            args.probe
        );
    } else {
        println!(
            "schema `{current_db}` 存在基准表 `{}`(或登记表)→ 判定为存量库,执行 baseline 登记。",
            args.probe
        );
    }

    if args.dry_run {
        println!("--dry-run:仅列出将登记/执行的版本");
        for m in migrator.iter().filter(|m| m.migration_type.is_up_migration()) {
            println!("  v{} {}", m.version, m.sql);
        }
        return Ok(());
    }

    // ── ② 无条件创建登记表(全新库也没有这张表)───────────────────
    // 顺序很关键:`dirty_version()` 与下面的 SELECT 都直接打这张表,
    // 表不存在时 MySQL 返回 1146 而不是空结果集,存量库会在「① 判定」之后就炸掉。
    // `Migrator::run` 内部也会建表,但它是在 dirty 检查之后,救不了我们自己这次调用。
    conn.ensure_migrations_table()
        .await
        .context("创建 _sqlx_migrations")?;

    let registered = if fresh {
        // ── 全新库:不登记任何历史版本,直接跑全部迁移 ──────────
        0usize
    } else {
        // ── ③ 校验并登记(逐条)────────────────────────────────
        // 登记表已在上一步无条件建好,此处只处理历史版本的对账。
        let mut registered = 0usize;
        let mut unverified = 0usize;
        for m in migrator.iter().filter(|m| m.migration_type.is_up_migration()) {
            let file_sum: &[u8] = m.checksum.as_ref();
            let existing: Option<(Vec<u8>,)> = sqlx::query_as(
                "SELECT checksum FROM _sqlx_migrations WHERE version = ?",
            )
            .bind(m.version)
            .fetch_optional(&mut conn)
            .await?;

            // 物理核验放在 checksum 校验之前:**登记本身就是断言 DDL 已生效**,
            // 一个「已登记但对象不存在」的版本必须被判脏,而不是照单全收。
            let proven = verify_objects(&mut conn, &current_db, &m.sql).await?;
            if !proven {
                // 未登记 → `Migrator::run()` 会真正执行它。
                // 跨过检查的是 INSERT IGNORE 之类的数据迁移(无结构证据可查),
                // 重复执行的代价是插入一行权限数据,可接受,且已由 IGNORE 兜底。
                // 已在登记表里的不重复播报:存量库每次重跑都会走一遍,
                // 刷屏会盖掉真正需要人看的「对象缺失」。
                if existing.is_none() {
                    unverified += 1;
                }
                continue;
            }

            match existing {
                // AppliedMigration 只有 {version, checksum},无 success —— 冲突即中止
                Some((checksum,)) => {
                    if checksum != file_sum {
                        bail!(
                            "版本 {} 的 checksum 不一致(库内 {} vs 文件 {}),需人工裁决,禁止静默跳过",
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
        if unverified > 0 {
            println!("  {unverified} 条数据迁移(INSERT/DELETE 等)无结构证据,交由 migrator 幂等重放");
        }
        registered
    };

    // ── ④ dirty 检查(两条分支共用)───────────────────────────────
    // 失败迁移检查
    if let Some(dirty) = conn.dirty_version().await? {
        bail!("schema 处于 dirty 状态(迁移 {dirty} 失败),请先人工修复,禁止继续");
    }

    // ── ⑤ 执行剩余迁移 ─────────────────────────────────────────
    // 全新库走到这里才是**第一次真正建表**;存量库则应用登记之后新增的版本。
    // 少了这一步,工具会以 0 退出码"成功"结束,业务服务随后连一张表都找不到。
    migrator
        .run(&mut conn)
        .await
        .with_context(|| format!("执行迁移 {}", args.source.display()))?;

    println!("登记完成:新增 {registered} 条;全量迁移执行完毕。");
    Ok(())
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// 一条迁移应当产生哪些物理对象。
///
/// 只认能从 SQL 里**确定解析**出来的对象 —— 解析不出来就返回空列表,
/// 由调用方决定策略(见 `verify_objects`)。宁可漏核验也不猜:
/// 猜错会把一个从未执行的迁移登记成成功,后果正是 V9 要防的那类事故。
#[derive(Debug)]
enum ExpectedObject {
    Table(String),
    Column { table: String, column: String },
}

/// 从迁移 SQL 中提取应当存在的物理对象。
///
/// 覆盖仓库里实际出现的三类写法:
///   - `CREATE TABLE [IF NOT EXISTS] <name>` —— 反引号可有可无
///   - `ALTER TABLE <name> ... ADD [COLUMN] <name>` —— 可多列,且常跨行书写
///   - `INSERT ...` / `MODIFY COLUMN` / `DELETE` —— 不产生新结构,不提取
///
/// 先把整段 SQL 压成单行再扫:迁移文件普遍把列定义拆到多行,
/// 逐行解析只能看到 `ADD COLUMN` 而看不到它后面跟的名字。
/// 同时先剥掉 `--` 行注释,否则注释里提到的表名会被当成真对象。
///
/// 不处理 `ALTER TABLE ... ADD UNIQUE KEY`(仓库里唯一的实例是
/// `user_db/0018`,它同时 ADD 了可提取的列,由列承担核验责任)。
fn expected_objects(sql: &str) -> Vec<ExpectedObject> {
    let flat = flatten_sql(sql);
    let mut out = Vec::new();
    let upper = flat.to_ascii_uppercase();

    let mut search_from = 0usize;
    while let Some(rel) = upper[search_from..].find("CREATE TABLE") {
        let pos = search_from + rel;
        let after = &flat[pos + "CREATE TABLE".len()..];
        // TEMPORARY / IF NOT EXISTS 都是多词修饰语，不能用 strip_prefix ——
        // 压平后是 "IF NOT EXISTS `name`"，必须按词边界跳过。
        let after = skip_keyword_phrase(after, "TEMPORARY");
        let after = skip_keyword_phrase(after, "IF NOT EXISTS");
        if let Some(name) = first_identifier(after) {
            out.push(ExpectedObject::Table(name));
        }
        search_from = pos + "CREATE TABLE".len();
    }

    let mut search_from = 0usize;
    while let Some(rel) = upper[search_from..].find("ALTER TABLE") {
        let pos = search_from + rel;
        let after = &flat[pos + "ALTER TABLE".len()..];
        if let Some(table) = first_identifier(after) {
            for column in added_columns(after) {
                out.push(ExpectedObject::Column { table: table.clone(), column });
            }
        }
        search_from = pos + "ALTER TABLE".len();
    }

    out
}

/// 把整段 SQL 压成一行：剥掉 `--` 行注释，把换行与连续空白折叠成单个空格。
///
/// MySQL 的注释规则是 `--` 后必须跟空白或行尾，`--x` 不是注释；
/// 这里按「`--` 后是空白或行尾」判定，避免误删表达式里的减号。
fn flatten_sql(sql: &str) -> String {    let mut out = String::with_capacity(sql.len());
    for line in sql.lines() {
        let trimmed = line.trim();
        let mut cut = trimmed.len();
        let bytes = trimmed.as_bytes();
        let mut i = 0usize;
        while i + 1 < bytes.len() {
            if bytes[i] == b'-' && bytes[i + 1] == b'-' {
                let after = bytes.get(i + 2).copied().unwrap_or(b'\n');
                if after.is_ascii_whitespace() {
                    cut = i;
                    break;
                }
            }
            i += 1;
        }
        let stmt = trimmed[..cut].trim();
        if stmt.is_empty() {
            continue;
        }
        if !out.is_empty() {
            out.push(' ');
        }
        out.push_str(stmt);
    }
    out
}

/// 若 `s` 以 `keyword` 开头（忽略大小写、且要求词边界），返回跳过该短语后的切片。
///
/// 词边界是指关键字后必须是空白或串尾，防止 `IFX` 被当成 `IF` 跳过。
fn skip_keyword_phrase<'a>(s: &'a str, keyword: &str) -> &'a str {
    // 调用方传进来的通常是 " IF NOT EXISTS `name`" 这种带前导空白的切片，
    // 不先 trim 就永远匹配不上关键字。
    let s = s.trim_start();
    let upper = s.to_ascii_uppercase();
    // 只取长度，切片仍从原串 s 上切，避免借用临时的大写副本。
    if !upper.starts_with(keyword) {
        return s;
    }
    let after = &s[keyword.len()..];
    if after.chars().next().is_some_and(|c| !c.is_whitespace()) {
        return s;
    }
    after
}

/// 取第一个 MySQL 标识符：允许 `` `name` `` / `"name"` / 裸 `name`。
fn first_identifier(s: &str) -> Option<String> {
    let s = s.trim_start();
    let mut chars = s.chars();
    match chars.next() {
        Some(q @ ('`' | '"')) => {
            let rest = chars.as_str();
            rest.find(q).map(|end| rest[..end].to_string())
        }
        Some(_) => {
            // 裸标识符：吃到空白或括号为止。
            let end = s.find(|c: char| c.is_whitespace() || c == '(').unwrap_or(s.len());
            let name = s[..end].trim_end_matches(|c: char| c == ';' || c == ',').trim();
            if name.is_empty() { None } else { Some(name.to_string()) }
        }
        None => None,
    }
}

/// 从 `ALTER TABLE <table> ...` 的尾部片段里取出所有 `ADD [COLUMN] <name>`。
///
/// 只认 `ADD COLUMN` 与光秃秃的 `ADD <name>`；`ADD UNIQUE KEY` / `ADD PRIMARY KEY` /
/// `ADD INDEX` / `ADD CONSTRAINT` 是约束而非新列，一律跳过 —— 仓库里唯一依赖约束的
/// 迁移（`user_db/0018`）同时 ADD 了可提取的列，由列承担核验责任。
fn added_columns(tail: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut search_from = 0usize;
    while let Some(rel) = tail[search_from..].to_ascii_uppercase().find("ADD ") {
        let pos = search_from + rel;
        let after = &tail[pos + "ADD ".len()..];
        let upper_after = after.to_ascii_uppercase();
        let keyword_len = if upper_after.starts_with("COLUMN") { "COLUMN ".len() } else { 0 };
        let candidate = after[keyword_len..].trim_start_matches("IF NOT EXISTS").trim_start();
        let candidate_upper = candidate.to_ascii_uppercase();
        let is_constraint = ["UNIQUE", "PRIMARY", "KEY", "INDEX", "CONSTRAINT", "FULLTEXT", "SPATIAL"]
            .iter()
            .any(|kw| candidate_upper.starts_with(kw));
        if !is_constraint {
            if let Some(name) = first_identifier(candidate) {
                out.push(name);
            }
        }
        search_from = pos + "ADD ".len();
    }
    out
}

/// 核验一条迁移应当产生的物理对象是否都已存在。
///
/// 返回 `false` 表示**证据不足或明确缺失**，调用方必须**不登记**该版本。
/// `INSERT`/`UPDATE` 之类没有结构证据可查，证据不足即不登记，
/// 由 `Migrator::run()` 实际执行（它们都是 `INSERT IGNORE`，重复执行安全）。
async fn verify_objects(
    conn: &mut MySqlConnection,
    schema: &str,
    sql: &str,
) -> Result<bool> {
    let expected = expected_objects(sql);
    if expected.is_empty() {
        return Ok(false);
    }
    for obj in &expected {
        let (table, column) = match obj {
            ExpectedObject::Table(t) => (t.clone(), None),
            ExpectedObject::Column { table, column } => (table.clone(), Some(column.clone())),
        };
        let count: i64 = match &column {
            Some(col) => sqlx::query_scalar(
                "SELECT COUNT(*) FROM information_schema.columns
                  WHERE table_schema = ? AND table_name = ? AND column_name = ?",
            )
            .bind(schema)
            .bind(&table)
            .bind(col)
            .fetch_one(&mut *conn)
            .await?,
            None => sqlx::query_scalar(
                "SELECT COUNT(*) FROM information_schema.tables
                  WHERE table_schema = ? AND table_name = ?",
            )
            .bind(schema)
            .bind(&table)
            .fetch_one(&mut *conn)
            .await?,
        };
        if count == 0 {
            println!("  对象缺失: {table} {column:?} → 版本不可登记");
            return Ok(false);
        }
    }
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_create_table_with_and_without_backticks() {
        let objs = expected_objects("CREATE TABLE IF NOT EXISTS `event_outbox` (\n  `id` BIGINT\n)");
        assert!(matches!(&objs[..], [ExpectedObject::Table(t)] if t == "event_outbox"), "实际解析: {objs:?}");

        let objs = expected_objects("CREATE TABLE vendor (\n  id BIGINT\n)");
        assert!(matches!(&objs[..], [ExpectedObject::Table(t)] if t == "vendor"));
    }

    #[test]
    fn extracts_multiple_added_columns() {
        let sql = "ALTER TABLE device_import\n  ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0,\n  ADD COLUMN retryable BOOLEAN NOT NULL DEFAULT TRUE,";
        let objs = expected_objects(sql);
        let names: Vec<&str> = objs
            .iter()
            .filter_map(|o| match o {
                ExpectedObject::Column { table, column } => {
                    assert_eq!(table, "device_import");
                    Some(column.as_str())
                }
                _ => None,
            })
            .collect();
        assert_eq!(names, vec!["attempts", "retryable"]);
    }

    #[test]
    fn extracts_backticked_column_added_to_backticked_table() {
        let sql = "ALTER TABLE `user`\n  ADD COLUMN nickname VARCHAR(64) DEFAULT NULL;";
        let objs = expected_objects(sql);
        assert!(
            matches!(&objs[..], [ExpectedObject::Column { table, column }] if table == "user" && column == "nickname"),
            "实际解析: {objs:?}"
        );
    }

    #[test]
    fn ignores_key_only_alter() {
        let sql = "ALTER TABLE `user`\n  DROP INDEX `idx_phone_hash`,\n  ADD UNIQUE KEY `uk_phone_hash` (`phone_hash`);";
        // 只有 UNIQUE KEY，没有可提取的 ADD COLUMN → 证据不足
        assert!(expected_objects(sql).is_empty());
    }

    #[test]
    fn skips_comment_lines() {
        let sql = "-- CREATE TABLE fake_table (\nCREATE TABLE real_table (\n  id BIGINT\n);";
        let objs = expected_objects(sql);
        assert!(matches!(&objs[..], [ExpectedObject::Table(t)] if t == "real_table"));
    }

    #[test]
    fn insert_only_migration_has_no_structural_evidence() {
        assert!(expected_objects("INSERT IGNORE INTO permission (code) VALUES ('x');").is_empty());
    }
}
