//! Read-only order queries owned by user_db. No cross-schema SQL.
use crate::AppState;
use api_contracts::orders::{OrderDetail, OrderPage, OrderQuery, OrderSummary};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use chrono::{DateTime, Duration, NaiveDateTime, SecondsFormat, Utc};
use common_auth::UserClaims;
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::{Deserialize, Serialize};
use sqlx::{mysql::MySqlRow, MySql, QueryBuilder, Row};

const SELECT_ORDER: &str = "SELECT c.id, c.order_no, c.user_id, c.device_id, c.port_no, c.status,
    c.created_at, c.started_at, c.ended_at, c.charged_seconds, CAST(c.charged_kwh AS CHAR) AS meter_kwh,
    c.electric_cents, c.service_cents, c.total_cents, c.failure_reason, c.payment_order_id,
    p.order_no AS payment_order_no, p.status AS payment_status, p.paid_cents, p.refunded_cents
    FROM charge_order c LEFT JOIN payment_order p ON p.id = c.payment_order_id AND p.deleted_at IS NULL";

struct Filters {
    from: NaiveDateTime,
    to: NaiveDateTime,
    status: Option<String>,
    devices: Option<Vec<String>>,
}

fn validate(query: &OrderQuery) -> AppResult<Filters> {
    if query.page == 0 || !(1..=100).contains(&query.page_size) {
        return Err(AppError::BadRequest(
            "page 必须大于 0，page_size 必须在 1–100 之间".into(),
        ));
    }
    if query.station_id.is_some() {
        return Err(AppError::BadRequest(
            "站点筛选必须由 admin 解析为设备列表".into(),
        ));
    }
    let parse = |value: &str| {
        DateTime::parse_from_rfc3339(value)
            .map(|date| date.naive_utc())
            .map_err(|_| AppError::BadRequest("时间必须为带时区的 ISO 8601 格式".into()))
    };
    let to = query
        .started_to
        .as_deref()
        .map(parse)
        .transpose()?
        .unwrap_or_else(|| Utc::now().naive_utc());
    let from = query
        .started_from
        .as_deref()
        .map(parse)
        .transpose()?
        .unwrap_or(to - Duration::days(7));
    if from > to {
        return Err(AppError::BadRequest("开始时间不能晚于结束时间".into()));
    }
    let status = query.status.as_deref().map(|status| {
        if status == "finished" {
            "completed"
        } else {
            status
        }
    });
    if let Some(status) = status {
        if ![
            "pending_payment",
            "paid",
            "charging",
            "completed",
            "cancelled",
            "failed",
            "refunding",
            "refunded",
        ]
        .contains(&status)
        {
            return Err(AppError::BadRequest("无效的订单状态".into()));
        }
    }
    for value in [query.order_no.as_deref(), query.device_id.as_deref()]
        .into_iter()
        .flatten()
    {
        if value.is_empty() || value.len() > 64 {
            return Err(AppError::BadRequest(
                "订单号和设备编号长度必须为 1–64".into(),
            ));
        }
    }
    let devices = query.device_ids.as_deref().map(|ids| {
        ids.split(',')
            .filter(|id| !id.is_empty())
            .map(str::to_owned)
            .collect()
    });
    Ok(Filters {
        from,
        to,
        status: status.map(str::to_owned),
        devices,
    })
}

fn conditions<'a>(
    builder: &mut QueryBuilder<'a, MySql>,
    query: &'a OrderQuery,
    filters: &'a Filters,
) {
    builder
        .push(" WHERE c.deleted_at IS NULL AND COALESCE(c.started_at,c.created_at) >= ")
        .push_bind(filters.from)
        .push(" AND COALESCE(c.started_at,c.created_at) <= ")
        .push_bind(filters.to);
    if let Some(status) = &filters.status {
        builder.push(" AND c.status = ").push_bind(status);
    }
    if let Some(order_no) = &query.order_no {
        builder.push(" AND c.order_no = ").push_bind(order_no);
    }
    if let Some(device_id) = &query.device_id {
        builder.push(" AND c.device_id = ").push_bind(device_id);
    }
    if let Some(devices) = &filters.devices {
        if devices.is_empty() {
            builder.push(" AND 1 = 0");
        } else {
            builder.push(" AND c.device_id IN (");
            let mut separated = builder.separated(",");
            for device in devices {
                separated.push_bind(device);
            }
            separated.push_unseparated(")");
        }
    }
}

fn timestamp(value: NaiveDateTime) -> String {
    value.and_utc().to_rfc3339_opts(SecondsFormat::Millis, true)
}

fn summary(row: &MySqlRow) -> AppResult<OrderSummary> {
    let status: String = row.try_get("status")?;
    let refunded: Option<i64> = row.try_get("refunded_cents")?;
    let paid: Option<i64> = row.try_get("paid_cents")?;
    let refund_status = if status == "refunding" {
        "processing"
    } else if refunded.unwrap_or(0) > 0 && refunded >= paid {
        "refunded"
    } else if refunded.unwrap_or(0) > 0 {
        "partial_refunded"
    } else {
        "none"
    };
    Ok(OrderSummary {
        order_id: row.try_get("id")?,
        order_no: row.try_get("order_no")?,
        user_id: row.try_get("user_id")?,
        device_id: row.try_get("device_id")?,
        port_no: row.try_get("port_no")?,
        station_id: None,
        station_name: None,
        status,
        created_at: timestamp(row.try_get("created_at")?),
        started_at: row
            .try_get::<Option<NaiveDateTime>, _>("started_at")?
            .map(timestamp),
        ended_at: row
            .try_get::<Option<NaiveDateTime>, _>("ended_at")?
            .map(timestamp),
        duration_seconds: row.try_get("charged_seconds")?,
        meter_kwh: row.try_get("meter_kwh")?,
        electric_fee_cents: row.try_get("electric_cents")?,
        service_fee_cents: row.try_get("service_cents")?,
        total_fee_cents: row.try_get("total_cents")?,
        refund_status: refund_status.into(),
    })
}

pub async fn list(
    State(state): State<AppState>,
    Query(query): Query<OrderQuery>,
) -> AppResult<Json<ApiEnvelope<OrderPage>>> {
    list_owned(state, query, None).await
}

async fn list_owned(
    state: AppState,
    query: OrderQuery,
    owner: Option<u64>,
) -> AppResult<Json<ApiEnvelope<OrderPage>>> {
    let filters = validate(&query)?;
    let mut tx = state.db.pool().begin().await?;
    let mut count = QueryBuilder::new("SELECT COUNT(*) FROM charge_order c");
    conditions(&mut count, &query, &filters);
    if let Some(owner) = owner {
        count.push(" AND c.user_id = ").push_bind(owner);
    }
    let total: i64 = count.build_query_scalar().fetch_one(&mut *tx).await?;
    let mut select = QueryBuilder::new(SELECT_ORDER);
    conditions(&mut select, &query, &filters);
    if let Some(owner) = owner {
        select.push(" AND c.user_id = ").push_bind(owner);
    }
    select
        .push(" ORDER BY c.created_at DESC, c.id DESC LIMIT ")
        .push_bind(query.page_size)
        .push(" OFFSET ")
        .push_bind(u64::from(query.page - 1) * u64::from(query.page_size));
    let rows = select.build().fetch_all(&mut *tx).await?;
    let items = rows.iter().map(summary).collect::<AppResult<Vec<_>>>()?;
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        OrderPage {
            items,
            total: total as u64,
            page: query.page,
            page_size: query.page_size,
        },
        common_error::current_request_id(),
    )))
}

pub async fn detail(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<ApiEnvelope<OrderDetail>>> {
    read_detail(state, id, None).await
}

async fn read_detail(
    state: AppState,
    id: String,
    owner: Option<u64>,
) -> AppResult<Json<ApiEnvelope<OrderDetail>>> {
    if id.is_empty() || id.len() > 64 {
        return Err(AppError::BadRequest("无效的订单标识".into()));
    }
    let mut select = QueryBuilder::new(SELECT_ORDER);
    select.push(" WHERE c.deleted_at IS NULL AND ");
    if let Ok(id) = id.parse::<u64>() {
        select.push("c.id = ").push_bind(id);
    } else {
        select.push("c.order_no = ").push_bind(id);
    }
    if let Some(owner) = owner {
        select.push(" AND c.user_id = ").push_bind(owner);
    }
    let row = select
        .build()
        .fetch_optional(state.db.pool())
        .await?
        .ok_or_else(|| AppError::NotFound("订单不存在".into()))?;
    let detail = OrderDetail {
        refund_applicant_id: None,
        order: summary(&row)?,
        payment_order_id: row.try_get("payment_order_id")?,
        payment_order_no: row.try_get("payment_order_no")?,
        payment_status: row.try_get("payment_status")?,
        paid_cents: row.try_get("paid_cents")?,
        refunded_cents: row.try_get("refunded_cents")?,
        failure_reason: row.try_get("failure_reason")?,
        billing: None,
    };
    Ok(Json(ApiEnvelope::ok(
        detail,
        common_error::current_request_id(),
    )))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UserHistoryQuery {
    pub page: Option<u32>,
    pub page_size: Option<u32>,
    pub status: Option<String>,
}

pub async fn user_history(
    State(state): State<AppState>,
    claims: UserClaims,
    Query(query): Query<UserHistoryQuery>,
) -> AppResult<Json<ApiEnvelope<OrderPage>>> {
    let query = OrderQuery {
        page: query.page.unwrap_or(1),
        page_size: query.page_size.unwrap_or(20),
        status: query.status,
        station_id: None,
        started_from: Some("1970-01-01T00:00:00Z".into()),
        started_to: None,
        order_no: None,
        device_id: None,
        device_ids: None,
    };
    list_owned(state, query, Some(claims.user_id)).await
}

#[derive(Serialize)]
pub struct UserOrderDetail {
    #[serde(flatten)]
    pub order: OrderSummary,
    pub paid_fee_cents: Option<i64>,
    pub refunded_cents: Option<i64>,
    pub payment_order_no: Option<String>,
    pub failure_reason: Option<String>,
}

pub async fn user_detail(
    State(state): State<AppState>,
    claims: UserClaims,
    Path(id): Path<String>,
) -> AppResult<Json<ApiEnvelope<UserOrderDetail>>> {
    // Ownership is enforced in SQL before any downstream financial lookup.
    let Json(envelope) = read_detail(state.clone(), id, Some(claims.user_id)).await?;
    let detail = envelope
        .data
        .ok_or_else(|| AppError::Internal("missing order detail".into()))?;
    let mut order = detail.order;
    let billing: api_contracts::orders::OrderBilling =
        common_http::internal::ApiClient::new(state.http.clone(), state.service_token.clone())
            .get(
                state.cfg.service_urls.billing.as_deref(),
                &api_contracts::paths::BILLING_ORDER_SUMMARY
                    .replace(":order_id", &order.order_id.to_string()),
                &(),
            )
            .await?;
    if billing.calculation_no.is_some() {
        order.electric_fee_cents = billing.electric_cents;
        order.service_fee_cents = billing.service_cents;
        order.total_fee_cents = billing.total_cents;
    }
    // Settlement participants belong to operators, not the end user's response.
    Ok(Json(ApiEnvelope::ok(
        UserOrderDetail {
            order,
            paid_fee_cents: detail.paid_cents,
            refunded_cents: detail.refunded_cents,
            payment_order_no: detail.payment_order_no,
            failure_reason: detail.failure_reason,
        },
        common_error::current_request_id(),
    )))
}

#[cfg(test)]
mod tests {
    use super::*;
    fn query() -> OrderQuery {
        serde_json::from_str("{}").unwrap()
    }
    #[test]
    fn pagination_and_time_validation() {
        let mut q = query();
        assert!(validate(&q).is_ok());
        q.page = 0;
        assert!(validate(&q).is_err());
        q.page = 1;
        q.page_size = 101;
        assert!(validate(&q).is_err());
        q.page_size = 20;
        q.started_from = Some("2026-09-25T12:00:00+08:00".into());
        q.started_to = Some("2026-09-25T03:00:00Z".into());
        assert!(validate(&q).is_err());
        q.started_to = Some("2026-09-25T05:00:00Z".into());
        assert!(validate(&q).is_ok());
    }
    #[test]
    fn status_alias_and_empty_station_are_not_unfiltered() {
        let mut q = query();
        q.status = Some("finished".into());
        q.device_ids = Some(String::new());
        let filters = validate(&q).unwrap();
        assert_eq!(filters.status.as_deref(), Some("completed"));
        let mut sql = QueryBuilder::new("SELECT c.id FROM charge_order c");
        conditions(&mut sql, &q, &filters);
        assert!(sql.sql().contains("AND 1 = 0"));
        q.status = Some("anything".into());
        assert!(validate(&q).is_err());
    }
}
