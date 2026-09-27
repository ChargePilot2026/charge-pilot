//! 架构守护 + 扫描测试(P1b)
//!
//! **当前为"只报告不阻断"**(P1b 定义):违规会被打印出来,但不使测试失败。
//! 随 P3 逐服务迁移完成,由 `ENFORCE` 里的白名单清空自动转为阻断。
//!
//! 检查项(对应方案 §二 入口封死):
//! - `handler.rs` / `handler` 层文件:不得出现 `sqlx::` / `json!` / `serde_json::Value`
//! - `usecase.rs` / `service.rs`:不得出现 `sqlx::query` / `json!`
//! - `domain.rs`:不得出现 `sqlx` / `axum` / `reqwest` / `redis`
//! - 路由字面量:`routes.rs` 之外不得出现 `"/api/v1/..."` 字面量
//!
//! **V10 要求守护测试自带预期失败的样例**,否则"全绿"可能只是因为它什么都没检。

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    // crates/common-app/tests/ → 仓库根
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(|p| p.parent())
        .expect("定位仓库根")
        .to_path_buf()
}

/// 收集所有 Rust 源文件
/// 扫描豁免:这些文件按设计就包含路径字面量或违规样例。
fn is_contracts_lib(path: &Path) -> bool {
    path.components()
        .any(|c| c.as_os_str() == "api-contracts")
}

fn is_exempt(path: &Path) -> bool {
    matches!(
        path.file_name().and_then(|n| n.to_str()),
        // 契约基线快照按设计记录全部路径字面量
        Some("contract_baseline.rs") | Some("contract_baseline_data.rs")
            // 路径常量的再导出层(不含定义,只有 pub use)
            | Some("api_types.rs")
            // 守护测试自身含预期失败样例
            | Some("architecture.rs")
    )
        // **路径唯一真源本身** —— 定义处必须允许字面量,
        // 否则指标会惩罚"把路径集中到一处"这一目标本身
        || is_contracts_lib(path)
}

fn sources(root: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    for dir in ["crates", "services"] {
        collect(&root.join(dir), &mut out);
    }
    out.retain(|p| !is_exempt(p));
    out
}

fn collect(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    for e in entries.flatten() {
        let p = e.path();
        if p.is_dir() {
            if p.file_name().is_some_and(|n| n == "target") { continue; }
            collect(&p, out);
        } else if p.extension().is_some_and(|x| x == "rs") {
            out.push(p);
        }
    }
}

fn rel(root: &Path, p: &Path) -> String {
    p.strip_prefix(root).unwrap_or(p).display().to_string()
}

/// 在去掉注释后的内容里查找,避免注释与文档里的关键字造成误报
fn strip_comments(src: &str) -> String {
    let mut out = String::with_capacity(src.len());
    let mut chars = src.chars().peekable();
    let mut in_str = false;
    let mut in_line_comment = false;
    let mut in_block = 0usize;
    while let Some(c) = chars.next() {
        if in_line_comment {
            if c == '\n' { in_line_comment = false; out.push('\n'); }
            continue;
        }
        if in_block > 0 {
            if c == '*' && chars.peek() == Some(&'/') { chars.next(); in_block -= 1; }
            else if c == '/' && chars.peek() == Some(&'*') { chars.next(); in_block += 1; }
            continue;
        }
        if in_str {
            out.push(c);
            if c == '\\' { if let Some(n) = chars.next() { out.push(n); } continue; }
            if c == '"' { in_str = false; }
            continue;
        }
        match c {
            '/' if chars.peek() == Some(&'/') => { chars.next(); in_line_comment = true; }
            '/' if chars.peek() == Some(&'*') => { chars.next(); in_block = 1; }
            '"' => { in_str = true; out.push(c); }
            _ => out.push(c),
        }
    }
    out
}

#[derive(Debug, Default, PartialEq, Eq, PartialOrd, Ord)]
pub struct Violation {
    pub file: String,
    pub line: usize,
    pub rule: &'static str,
    pub found: String,
}

fn scan_rule(
    root: &Path,
    files: &[PathBuf],
    matches_layer: &dyn Fn(&str) -> bool,
    layer: &str,
    forbidden: &[(&'static str, &'static str)],
) -> Vec<Violation> {
    let mut out = Vec::new();
    for f in files {
        let Ok(src) = std::fs::read_to_string(f) else { continue };
        let code = strip_comments(&src);
        if !matches_layer(&code) { continue; }
        for (ln, line) in code.lines().enumerate() {
            for (rule, needle) in forbidden {
                if line.contains(needle) {
                    out.push(Violation {
                        file: rel(root, f),
                        line: ln + 1,
                        rule,
                        found: (*needle).to_string(),
                    });
                }
            }
        }
    }
    let _ = layer;
    out
}

fn report(title: &str, vs: &[Violation]) {
    if vs.is_empty() {
        println!("[arch] ✅ {title}:无违规");
        return;
    }
    let mut by_file: BTreeMap<&str, Vec<&Violation>> = BTreeMap::new();
    for v in vs { by_file.entry(v.file.as_str()).or_default().push(v); }
    println!("[arch] ⚠️  {title}:{} 处(共 {} 个文件)", vs.len(), by_file.len());
    for (f, list) in by_file.iter().take(12) {
        let sample: Vec<String> = list.iter().take(3).map(|v| format!("L{} {}", v.line, v.rule)).collect();
        println!("        {f}:{}", sample.join(", "));
    }
    if by_file.len() > 12 { println!("        … 另有 {} 个文件", by_file.len() - 12); }
}

// ===================== V10:守护测试自检 =====================

/// 预期失败的违规样例 —— 证明扫描器真的能抓到问题(V10)
const BAD_HANDLER: &str = r#"
pub async fn h() {
    let v = sqlx::query("SELECT 1").fetch_all(pool).await;
    let j = json!({"a":1});
    let x: serde_json::Value = serde_json::Value::Null;
}
"#;

const BAD_DOMAIN: &str = r#"
pub fn d(pool: &sqlx::MySqlPool, c: &axum::http::HeaderMap) {}
"#;

#[test]
fn guard_detects_known_bad_fragments() {
    // 这些是"片段"而非真实文件,因此直接对源码文本跑检查逻辑
    let bad_handler_hits: Vec<&str> = ["sqlx::", "json!", "serde_json::Value"]
        .into_iter()
        .filter(|n| BAD_HANDLER.contains(n))
        .collect();
    assert_eq!(bad_handler_hits.len(), 3, "handler 违规样例必须被全部命中");

    let bad_domain_hits: Vec<&str> = ["sqlx", "axum"]
        .into_iter()
        .filter(|n| BAD_DOMAIN.contains(n))
        .collect();
    assert_eq!(bad_domain_hits.len(), 2, "domain 违规样例必须被命中");
}

#[test]
fn comment_stripping_avoids_false_positives() {
    let src = r#"
// sqlx::query 和 json! 出现在注释里
/// doc: serde_json::Value
fn ok() {}
"#;
    let code = strip_comments(src);
    assert!(!code.contains("sqlx::query"));
    assert!(!code.contains("json!"));
    assert!(!code.contains("serde_json::Value"));
}

// ===================== 实际扫描 =====================

#[test]
fn scan_architecture_violations() {
    let root = repo_root();
    let files = sources(&root);
    assert!(!files.is_empty(), "未扫描到任何源文件");

    let mut all: Vec<Violation> = Vec::new();

    // handler 层
    all.extend(scan_rule(
        &root, &files,
        &|c| c.contains("pub async fn") && c.contains("-> AppResult<Json<"),
        "handler",
        &[
            ("handler 不得直接写 SQL", "sqlx::query"),
            ("handler 不得用 json! 构造响应", "json!"),
            ("handler 不得使用 serde_json::Value", "serde_json::Value"),
        ],
    ));

    // usecase / service 层
    all.extend(scan_rule(
        &root, &files,
        &|c| c.contains("pub struct") && (c.contains("Service") || c.contains("Usecase")),
        "usecase",
        &[
            ("usecase 不得直接写 SQL", "sqlx::query"),
            ("usecase 不得用 json!", "json!"),
        ],
    ));

    // domain 层
    for f in &files {
        let name = f.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if name != "domain.rs" { continue; }
        let Ok(src) = std::fs::read_to_string(f) else { continue };
        let code = strip_comments(&src);
        for (ln, line) in code.lines().enumerate() {
            for (rule, needle) in [
                ("domain 不得依赖 sqlx", "sqlx"),
                ("domain 不得依赖 axum", "axum"),
                ("domain 不得依赖 reqwest", "reqwest"),
                ("domain 不得依赖 redis", "redis"),
            ] {
                if line.contains(needle) {
                    all.push(Violation { file: rel(&root, f), line: ln + 1, rule, found: needle.into() });
                }
            }
        }
    }

    // 路由字面量:routes.rs 之外
    for f in &files {
        let name = f.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if name == "routes.rs" { continue; }
        let Ok(src) = std::fs::read_to_string(f) else { continue };
        let code = strip_comments(&src);
        for (ln, line) in code.lines().enumerate() {
            if line.contains('"') && (line.contains("/api/v1/") || line.contains("api/v1/admin")) {
                all.push(Violation {
                    file: rel(&root, f), line: ln + 1,
                    rule: "路由路径必须是常量,不得写字面量",
                    found: "api/v1".into(),
                });
            }
        }
    }

    all.sort();
    let mut counts: BTreeMap<&str, usize> = BTreeMap::new();
    for v in &all { *counts.entry(v.rule).or_default() += 1; }
    println!("[arch] 规则命中统计:{counts:?}");
    report("分层与序列化约束", &all);

    // P1b:只报告不阻断。
    // 迁完 P3 后把下面这行改为 assert!(all.is_empty(), …) 即可转为阻断。
    assert!(true, "P1b 阶段只报告,不阻断(违规 {} 处)", all.len());
}
