//! PC 后台 React SPA 静态资源服务
//!
//! 实际部署:把 admin-web/dist 挂到 /app/static,fallback 返回 index.html

use axum::{
    body::Body,
    extract::Request,
    http::{header, StatusCode},
    response::{IntoResponse, Response},
};
use common_error::{AppError, AppResult};
use std::path::{Component, Path, PathBuf};
use tokio::fs;
use tracing::warn;

static SPA_DIR: &str = "/app/static";

/// 把请求路径解析为静态根目录内的真实路径。
///
/// D9 修复:原实现用 `PathBuf::starts_with` 做穿越防护,但它是**逐组件**比较——
/// `/app/static/../x` 的组件为 `["/", "app", "static", "..", "x"]`,以静态根开头
/// 因此检查通过,而 `fs::read` 会由操作系统消解 `..`。
///
/// 现在分三步:
/// 1. 逐组件拒绝 `..`(以及前缀/根组件)——不依赖任何规范化;
/// 2. 双方都 `canonicalize()` 后再做包含性校验——消解符号链接逃逸;
/// 3. 第 2 步在目标不存在时无法执行,此时只放行第 1 步已通过、且拼出路径仍在
///    根目录下的情况,交给调用方的读取分支去走 SPA fallback。
fn resolve_within(root: &Path, request_path: &str) -> AppResult<PathBuf> {
    let target = request_path.trim_start_matches('/');
    let relative = if target.is_empty() {
        "index.html"
    } else {
        target
    };

    // 1) 逐组件拒绝穿越
    for component in Path::new(relative).components() {
        match component {
            Component::Normal(_) => {}
            // `..`、`.` 之外的 CurDir/ParentDir/RootDir/Prefix 一律拒绝
            _ => return Err(AppError::BadRequest("bad path".into())),
        }
    }

    let joined = root.join(relative);

    // 2) 双方 canonicalize 后做包含性校验(消解符号链接)
    if let (Ok(real_root), Ok(real_target)) = (root.canonicalize(), joined.canonicalize()) {
        if !real_target.starts_with(&real_root) {
            return Err(AppError::BadRequest("bad path".into()));
        }
        return Ok(real_target);
    }

    // 3) 目标不存在:只放行拼出路径仍在根目录内的情况
    if !joined.starts_with(root) {
        return Err(AppError::BadRequest("bad path".into()));
    }
    Ok(joined)
}

pub async fn serve_spa(req: Request) -> Response {
    let file_path = match resolve_within(Path::new(SPA_DIR), req.uri().path()) {
        Ok(p) => p,
        Err(_) => return (StatusCode::BAD_REQUEST, "bad path").into_response(),
    };
    let resolved_path = req.uri().path().trim_start_matches('/').to_string();

    match fs::read(&file_path).await {
        Ok(bytes) => {
            let mime = mime_guess(&resolved_path);
            let mut resp = Response::new(Body::from(bytes));
            resp.headers_mut().insert(header::CONTENT_TYPE, mime.parse().unwrap_or_else(|_| "application/octet-stream".parse().unwrap()));
            resp.headers_mut().insert(header::CACHE_CONTROL, if resolved_path == "index.html" {
                "no-cache".parse().unwrap()
            } else {
                "public, max-age=86400".parse().unwrap()
            });
            resp
        }
        Err(_) => {
            // SPA fallback:返回 index.html
            let index = PathBuf::from(SPA_DIR).join("index.html");
            match fs::read(&index).await {
                Ok(bytes) => {
                    let mut resp = Response::new(Body::from(bytes));
                    resp.headers_mut().insert(header::CONTENT_TYPE, "text/html; charset=utf-8".parse().unwrap());
                    resp.headers_mut().insert(header::CACHE_CONTROL, "no-cache".parse().unwrap());
                    resp
                }
                Err(_) => {
                    warn!(path = %file_path.display(), "SPA assets not found, returning 404");
                    (StatusCode::NOT_FOUND, "ChargePilot admin SPA not built yet. Run: cd admin-web && npm run build").into_response()
                }
            }
        }
    }
}

fn mime_guess(p: &str) -> &'static str {
    match p.rsplit('.').next().unwrap_or("") {
        "html" => "text/html; charset=utf-8",
        "js" | "mjs" => "application/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" => "application/json; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "ico" => "image/x-icon",
        "woff2" => "font/woff2",
        "woff" => "font/woff",
        _ => "application/octet-stream",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    /// 建一个"真实存在"的静态根 + 一个根目录外的哨兵文件。
    /// 验收要求测试以真实静态目录为前提,否则会落进 index.html 读不到的分支。
    struct Fixture {
        root: PathBuf,
        outside: PathBuf,
    }

    impl Fixture {
        fn new() -> Self {
            let tag = uuid::Uuid::new_v4();
            let base = std::env::temp_dir().join(format!("d9-static-{tag}"));
            let root = base.join("static");
            let outside = base.join("outside");
            fs::create_dir_all(&root).unwrap();
            fs::create_dir_all(&outside).unwrap();
            fs::write(root.join("index.html"), b"<html>spa</html>").unwrap();
            fs::write(root.join("app.js"), b"console.log(1)").unwrap();
            fs::write(outside.join("sentinel.txt"), b"TOP SECRET").unwrap();
            Self { root, outside }
        }
    }

    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(self.root.parent().unwrap());
        }
    }

    #[test]
    fn allows_files_inside_root() {
        let f = Fixture::new();
        let p = resolve_within(&f.root, "/app.js").expect("根内文件应放行");
        assert!(p.starts_with(fs::canonicalize(&f.root).unwrap()));
        assert_eq!(fs::read(&p).unwrap(), b"console.log(1)");
    }

    #[test]
    fn empty_path_falls_back_to_index() {
        let f = Fixture::new();
        let p = resolve_within(&f.root, "/").expect("根路径应落到 index.html");
        assert!(p.ends_with("index.html"));
    }

    #[test]
    fn rejects_parent_dir_traversal() {
        let f = Fixture::new();
        // 这三个形式在旧实现下 `starts_with` 全部为 true(已复现)
        for path in ["/../sentinel.txt", "/../../etc/passwd", "/assets/../../sentinel.txt"] {
            assert!(
                resolve_within(&f.root, path).is_err(),
                "穿越请求 {path} 必须被拒绝"
            );
        }
    }

    #[test]
    fn rejects_cur_dir_component() {
        let f = Fixture::new();
        assert!(resolve_within(&f.root, "/./../outside/sentinel.txt").is_err());
    }

    /// D9 验收:符号链接指向目录外时同样不可读。
    /// 仅靠 starts_with 无法拦住这一类——文件名本身是根内组件。
    #[test]
    fn rejects_symlink_escaping_root() {
        #[cfg(unix)]
        {
            let f = Fixture::new();
            let link = f.root.join("escape.txt");
            std::os::unix::fs::symlink(f.outside.join("sentinel.txt"), &link).unwrap();
            let resolved = resolve_within(&f.root, "/escape.txt");
            assert!(
                resolved.is_err(),
                "指向根外的符号链接必须被拒绝,实际解析为 {resolved:?}"
            );
        }
    }

    #[test]
    fn nonexistent_target_inside_root_is_allowed_for_spa_fallback() {
        let f = Fixture::new();
        // SPA 前端路由需要:不存在的路径不能报错,要交给 index.html fallback
        let p = resolve_within(&f.root, "/dashboard/orders").expect("根内不存在路径应放行给 fallback");
        assert!(!fs::exists(&p).unwrap());
    }
}