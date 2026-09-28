//! order 域 —— 充电订单读
//!
//! 订单数据归 user / billing 服务所有,admin 只做**读**与站点信息补齐。
//! 本域的 [`super::order`] 没有可抽的纯逻辑:筛选条件直接来自
//! `api_contracts::orders::OrderQuery`(契约自带 `valid()`),其余是跨服务
//! 调用与行列映射。故不建 `domain.rs` —— 不制造空壳。

#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

// 本文件的 `sqlx::QueryBuilder` + `push_bind` 属方案 §三例外清单第 3 条:
// **动态 WHERE 拼装**。站点补齐要按 N 个 device_id 生成 `IN (?,?,…)`,
// 绑参数数量由数据决定,无法写成固定语句。`push_bind` 负责转义,
// 不存在拼接注入;被禁的是无绑定的 `QueryBuilder::new(&string)` 硬拼。

use crate::AppState;
use common_error::AppResult;
use sqlx::{MySql, QueryBuilder};
use std::collections::HashMap;

/// 订单页补齐站点信息:按 device_id 批量查 `device_meta` + `station`。
pub async fn stations(st: &AppState, items: &mut [api_contracts::orders::OrderSummary]) -> AppResult<()> {
    if items.is_empty() {
        return Ok(());
    }
    let mut query = QueryBuilder::<MySql>::new(
        "SELECT d.device_id, s.id, s.name FROM device_meta d JOIN station s ON s.id = d.station_id
         WHERE d.deleted_at IS NULL AND s.deleted_at IS NULL AND d.device_id IN (",
    );
    let mut ids = query.separated(",");
    for item in items.iter() {
        ids.push_bind(&item.device_id);
    }
    ids.push_unseparated(")");
    let rows: Vec<(String, u64, String)> =
        query.build_query_as().fetch_all(st.order.pool()).await?;
    let names: HashMap<_, _> = rows
        .into_iter()
        .map(|(device, id, name)| (device, (id, name)))
        .collect();
    for item in items {
        if let Some((id, name)) = names.get(&item.device_id) {
            item.station_id = Some(*id);
            item.station_name = Some(name.clone());
        }
    }
    Ok(())
}

/// 站点筛选 → 设备编号列表(逗号分隔,交给 user 侧按 device scope 查询)。
pub async fn device_ids_for_station(st: &AppState, station_id: u64) -> AppResult<Vec<String>> {
    let devices: Vec<String> = sqlx::query_scalar(
        "SELECT d.device_id FROM device_meta d JOIN station s ON s.id = d.station_id
         WHERE d.station_id = ? AND d.deleted_at IS NULL AND s.deleted_at IS NULL",
    )
    .bind(station_id)
    .fetch_all(st.order.pool())
    .await?;
    Ok(devices)
}
