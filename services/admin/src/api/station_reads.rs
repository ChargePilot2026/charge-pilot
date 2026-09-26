use crate::AppState;
use api_contracts::{
    NearbyStationItem, NearbyStationsQuery, NearbyStationsResponse, StationPublicDetail,
};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;

fn radius(q: &NearbyStationsQuery) -> AppResult<f64> {
    let radius = q.radius_km.unwrap_or(5.0);
    if !q.lat.is_finite()
        || !q.lng.is_finite()
        || !(-90.0..=90.0).contains(&q.lat)
        || !(-180.0..=180.0).contains(&q.lng)
        || !radius.is_finite()
        || !(0.1..=50.0).contains(&radius)
    {
        return Err(AppError::BadRequest(
            "经纬度无效或搜索半径不在 0.1–50 公里之间".into(),
        ));
    }
    Ok(radius)
}

pub async fn nearby(
    State(state): State<AppState>,
    Query(query): Query<NearbyStationsQuery>,
) -> AppResult<Json<ApiEnvelope<NearbyStationsResponse>>> {
    let radius = radius(&query)?;
    // Latitude bound reduces candidates; spherical longitude math also handles the date line.
    // Filter and sort BEFORE LIMIT so nearer stations are never discarded arbitrarily.
    let rows = sqlx::query("SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,
        6371.0088 * ACOS(LEAST(1e0,GREATEST(-1e0,SIN(RADIANS(?))*SIN(RADIANS(latitude)) + COS(RADIANS(?))*COS(RADIANS(latitude))*COS(RADIANS(longitude-?))))) AS distance_km
        FROM station WHERE status='active' AND deleted_at IS NULL AND latitude BETWEEN ? AND ?
        HAVING distance_km <= ? ORDER BY distance_km,id LIMIT 100")
        .bind(query.lat).bind(query.lat).bind(query.lng).bind(query.lat-radius/110.0).bind(query.lat+radius/110.0).bind(radius)
        .fetch_all(state.db.pool()).await?;
    let mut items = Vec::with_capacity(rows.len());
    for row in rows {
        items.push(NearbyStationItem {
            id: row.try_get("id")?,
            code: row.try_get("code")?,
            name: row.try_get("name")?,
            address: row.try_get("address")?,
            longitude: row.try_get("longitude")?,
            latitude: row.try_get("latitude")?,
            distance_km: row.try_get("distance_km")?,
        });
    }
    Ok(Json(ApiEnvelope::ok(
        NearbyStationsResponse { items },
        common_error::current_request_id(),
    )))
}

pub async fn detail(
    State(state): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<StationPublicDetail>>> {
    let row = sqlx::query("SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,open_hours,contact_phone FROM station WHERE id=? AND status='active' AND deleted_at IS NULL")
        .bind(id).fetch_optional(state.db.pool()).await?.ok_or_else(||AppError::NotFound("站点不存在或未开放".into()))?;
    Ok(Json(ApiEnvelope::ok(
        StationPublicDetail {
            id: row.try_get("id")?,
            code: row.try_get("code")?,
            name: row.try_get("name")?,
            address: row.try_get("address")?,
            longitude: row.try_get("longitude")?,
            latitude: row.try_get("latitude")?,
            open_hours: row.try_get("open_hours")?,
            contact_phone: row.try_get("contact_phone")?,
        },
        common_error::current_request_id(),
    )))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn rejects_invalid_geography() {
        for (lat, lng, r) in [
            (91.0, 0.0, 5.0),
            (0.0, 181.0, 5.0),
            (f64::NAN, 0.0, 5.0),
            (0.0, 0.0, 0.0),
            (0.0, 0.0, 51.0),
        ] {
            assert!(radius(&NearbyStationsQuery {
                lat,
                lng,
                radius_km: Some(r)
            })
            .is_err());
        }
        assert_eq!(
            radius(&NearbyStationsQuery {
                lat: 90.0,
                lng: 180.0,
                radius_km: None
            })
            .unwrap(),
            5.0
        );
    }
}
