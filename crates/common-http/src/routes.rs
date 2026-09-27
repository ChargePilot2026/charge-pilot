//! 路由注册表(P1a 骨架)
//!
//! **目的**:消灭"裸字符串路径"这种第三种路由风格。此前 185 个 `.route()` 里有
//! 20 处直接写字面量(gateway 14 / admin 6),与 `api_types::paths` /
//! `api_contracts::paths` 并列,路径拼错编译期发现不了。
//!
//! **P1a 只建不拆**:本文件提供注册表与类型化的路径常量,但**不替换**现有
//! `build_router`——那属于 P3 逐服务迁移。P3 起每个服务改为从本注册表构造路由,
//! 字面量入口随之关闭。
//!
//! 用法:
//! ```ignore
//! routes::register(Router::new(), routes::USER_SCAN_START, post(api::scan_start));
//! ```

use axum::routing::MethodRouter;
use axum::Router;

/// 路由条目:路径 + HTTP 方法,注册时三者绑定,避免路径与 handler 漂移。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Route {
    pub path: &'static str,
}

impl Route {
    pub const fn new(path: &'static str) -> Self {
        Self { path }
    }
}

// 路径常量**不在这里定义**——唯一真源是 `api-contracts::paths`。本模块只提供
// 注册机制,各服务把自己的契约常量传进来,避免出现第二份定义。

/// 把一条路径与它的 `MethodRouter` 绑定注册。
///
/// 字面量路径在这里被显式转换,其它地方无法直接 `.route("/api/…", …)`。
pub fn register(router: Router, route: Route, method: MethodRouter) -> Router {
    router.route(route.path, method)
}

/// 便于在测试与文档中枚举当前已注册路由
#[derive(Debug, Default)]
pub struct RouteTable {
    entries: Vec<(Route, &'static [&'static str])>,
}

impl RouteTable {
    pub fn new() -> Self {
        Self::default()
    }

    /// 登记一条路由及其方法名(仅用于可观测性,不做运行时校验)
    pub fn push(&mut self, route: Route, methods: &'static [&'static str]) {
        self.entries.push((route, methods));
    }

    pub fn paths(&self) -> impl Iterator<Item = &'static str> + '_ {
        self.entries.iter().map(|(r, _)| r.path)
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn route_keeps_its_path() {
        let r = Route::new("/api/v1/user/charge/start");
        assert_eq!(r.path, "/api/v1/user/charge/start");
    }

    #[test]
    fn route_table_collects_paths() {
        let mut t = RouteTable::new();
        assert!(t.is_empty());
        t.push(Route::new("/a"), &["GET"]);
        t.push(Route::new("/b"), &["POST"]);
        assert_eq!(t.len(), 2);
        let paths: Vec<_> = t.paths().collect();
        assert_eq!(paths, vec!["/a", "/b"]);
    }
}
