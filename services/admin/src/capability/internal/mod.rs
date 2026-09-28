//! internal 域 —— 跨服务内部端点(按调用方组织,不按业务域)
//!
//! **为什么单列一域而不是拆进 device / config / alert**:
//! 这些端点是"按调用方(内部服务)组织"的 —— `charge` 要站点、要公告、
//! 要客服入口、要告警;`billing` 要计费规则;`gateway` 要设备重启。
//! 硬塞进任一业务域都会让那个域的边界失真:同一个 `internal.rs` 会被按
//! 表归属劈到 device / config / alert / cases 四个目录,而每个目录里
//! 只剩一两个端点,业务域的目录结构反而看不出"这个域管什么"。
//!
//! 本域的**真实**共性是鉴权方式:全部走 `internal_token_mw`(`x-service-token`),
//! 不取 `ActiveAdmin`,与 PC 后台路由是两种信任模型。按信任模型切域是
//! 合理的一刀。SQL 仍全部在 `repository_sql.rs`。
//!
//! 域内仍保留"公告 / 客服 / 告警 / 计费 / 分账"各自的表级查询,
//! 只是入口统一放在这里 —— 读者顺着 `build_router` 找内部端点时,
//! 一处即可看全。

pub mod repository_sql;

use crate::AppState;
use axum::{extract::State, Json};
use common_error::AppResult;

pub use repository_sql::NearbyQuery;

// 公告 / 客服入口 / 告警 / 计费规则 / 分账模板的读端点,SQL 分别在各自业务域
// 的 repository 里 —— 它们与业务域内的其它读端点共用同一段语句与判定,
// 拆开只会写出两份 SQL。
pub use crate::capability::alert::active as alerts_active;
pub use crate::capability::config::{
    active_announcements, customer_service_entry, expire_announcements, pricing_rule_get,
    split_template_get, CustomerServiceEntryQuery,
};

pub async fn stations_nearby(
    State(st): State<AppState>,
    axum::extract::Query(q): axum::extract::Query<NearbyQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::NearbyStations>>> {
    let radius = q.radius_km.unwrap_or(5.0);
    let items = repository_sql::stations_nearby(&st, radius, q.lat, q.lng).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::NearbyStations { items },
        common_error::current_request_id(),
    )))
}

pub async fn stations_detail(
    State(st): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<u64>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::StationForUser>>> {
    let detail = repository_sql::station_for_user(&st, id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

/// gateway 桩端点不读请求体,但 `post_typed` 需要一个 `Serialize` 值。
/// 具名空结构体优于 `json!({})`:前者零宏、意图自明,且序列化结果是 `{}`
/// —— 与原实现发给 gateway 的字节完全一致。
#[derive(serde::Serialize)]
struct EmptyBody {}

/// 调 gateway 内部接口 — 类型化 client + 路径常量,禁止拼 URL。
pub async fn device_reboot(
    State(st): State<AppState>,
    axum::extract::Path(id): axum::extract::Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let path = api_contracts::fill_path(crate::api_types::paths::INTERNAL_DEVICES_REBOOT, "id", &id);
    // gateway 侧该端点是未接入的桩,响应类型即 NotImplementedResponse。
    let v: api_contracts::gateway_devices::NotImplementedResponse = cli
        .post_typed(st.cfg.service_urls.gateway.as_deref(), &path, &EmptyBody {})
        .await?;
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}
