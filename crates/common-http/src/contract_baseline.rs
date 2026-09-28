//! 对外契约基线快照(P1a)
//!
//! **目的**:重构期间接口会变,但**必须是"已登记的变"**。此快照记录改造前的
//! 路由与响应形态,作为 P2 契约重写与 P6 变更清单的对照基线。
//!
//! **⚠️ 已知错误行为不在基线内**——否则快照会把 bug 固化。见 `EXCLUDED`。

use std::collections::BTreeMap;

/// 一条对外路由
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BaselineRoute {
    pub method: &'static str,
    pub path: &'static str,
    pub handler_hint: &'static str,
}

/// 契约基线
#[derive(Debug, Default, Clone)]
pub struct ContractBaseline {
    pub routes: Vec<BaselineRoute>,
    /// 刻意排除在基线之外的条目(缺陷编号 → 说明)
    pub excluded: BTreeMap<&'static str, &'static str>,
}

impl ContractBaseline {
    /// 该路径的当前路由条目
    pub fn routes_for(&self, path: &str) -> Vec<&BaselineRoute> {
        self.routes.iter().filter(|r| r.path == path).collect()
    }

    pub fn method_paths(&self, method: &str) -> Vec<&'static str> {
        let mut out: Vec<&'static str> = self
            .routes
            .iter()
            .filter(|r| r.method.eq_ignore_ascii_case(method))
            .map(|r| r.path)
            .collect();
        out.sort_unstable();
        out.dedup();
        out
    }

    pub fn route_count(&self) -> usize {
        self.routes.len()
    }
}

/// 已知错误行为,**不纳入基线**(P1a 铁律:基线不得固化 bug)
pub const EXCLUDED: &[(&str, &str)] = &[
    ("D1", "账号/角色管理无操作授权:基线不含'任意登录态管理员可提权'这一行为"),
    ("D2", "停用/撤权后仍可续命:基线不含'旧 token 可续命'这一行为"),
    ("D3", "提现经跨库视图直写 billing 域;admin-web 零调用 —— 整块删除,不进基线"),
    ("D24", "停机回写 URL 的路径占位符名与常量不匹配,replace 静默失效 → 端点永不命中、计费链不触发；基线不含'正常工作的停机回写'这一事实"),
    ("D4", "Stream 裁剪丢未消费事件;ACK≠业务完成 —— 属可靠性缺陷,不作契约"),
    ("D5", "规则变更事件 DB 成功但事件永久丢失"),
    ("D6", "billing 查 user_db 的表:端点恒报错,不进基线"),
    ("D7", "双层信封:基线按单层信封记录"),
    ("D9", "静态资源路径穿越:基线不含'可读目录外文件'"),
    ("D10", "全额退款被计价错误截断:基线不含'跨电价退款单返回 Conflict'"),
    ("D11", "webhook_retry 全链路未实现(生产 payload 无 url)"),
    ("D13", "Argon2 阻塞 Tokio 执行线程 —— 非契约,属运行时缺陷"),
    ("D14", "开票以预付款为金额来源:基线不含'预付 1000 可开票 1000'"),
    ("D15", "阻塞消费与发布共享 Redis 连接"),
    ("D16", "跨分时电价订单无法计费(ChargeEndMeter 无分段读数)"),
    ("D17", "限流计数可能永不过期"),
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn baseline_lookup_by_path() {
        let b = ContractBaseline {
            routes: vec![
                BaselineRoute { method: "GET", path: "/api/v1/health", handler_hint: "api::health" },
                BaselineRoute { method: "POST", path: "/api/v1/user/scan/start", handler_hint: "api::scan_start" },
                BaselineRoute { method: "POST", path: "/api/v1/user/scan/start", handler_hint: "api::scan_start" },
            ],
            excluded: BTreeMap::new(),
        };
        assert_eq!(b.routes_for("/api/v1/user/scan/start").len(), 2);
        assert_eq!(b.method_paths("GET"), vec!["/api/v1/health"]);
    }

    /// 回归护栏:已修复的缺陷不得再被写回基线
    #[test]
    fn known_bad_behaviours_are_excluded() {
        let ids: Vec<&str> = EXCLUDED.iter().map(|(id, _)| *id).collect();
        for must in ["D1", "D2", "D9", "D10", "D14"] {
            assert!(ids.contains(&must), "{must} 必须登记在排除清单中");
        }
    }

    /// **D3：已删除的 6 条路由在基线里必须留痕**。
    ///
    /// 基线是「改造前实际注册」的历史快照。`withdraw` / `membership` 的
    /// 6 条路由**故意保留** —— 删掉它们等于篡改历史证据，日后就无法回答
    /// 「这批接口到底什么时候没的、为什么没的」。对账依据是 `EXCLUDED` 的 D3 条目
    /// 与 `docs/api-change-list.md` §1。
    #[test]
    fn d3_removed_routes_remain_in_baseline() {
        let removed: Vec<&str> = crate::contract_baseline_data::BASELINE
            .iter()
            .map(|r| r.path)
            .filter(|p| p.contains("withdraw") || p.contains("membership"))
            .collect();
        assert_eq!(
            removed.len(),
            6,
            "D3 应留下 6 条历史路由痕迹（admin 提现 3 + admin 会员卡 2 + billing 提现 1），实际 {removed:?}"
        );
        assert!(
            EXCLUDED.iter().any(|(id, _)| *id == "D3"),
            "D3 必须在 EXCLUDED 中有登记，否则快照与变更清单无法对账"
        );
    }
}
