//! Persist device ACKs without querying gateway tables or replacing another charge.
use api_contracts::StartResultRequest;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use sqlx::{MySql, Row, Transaction};

pub fn validate(path: &str, req: &StartResultRequest) -> AppResult<String> {
    if path != req.order_no
        || path.is_empty()
        || path.len() > 64
        || req.device_id.is_empty()
        || req.device_id.len() > 64
        || req.port_no == 0
        || req.port_id == Some(0)
        || (req.success && req.port_id.is_none())
        || req
            .error
            .as_ref()
            .is_some_and(|e| e.chars().count() > 255 || e.chars().any(char::is_control))
    {
        return Err(AppError::BadRequest(
            "设备启动结果缺少有效的订单或端口信息".into(),
        ));
    }
    uuid::Uuid::parse_str(&req.command_id)
        .map(|id| id.to_string())
        .map_err(|_| AppError::BadRequest("启动指令标识无效".into()))
}
fn conflict() -> AppError {
    AppError::Conflict("启动结果与订单或端口占用不一致".into())
}

pub async fn process(
    pool: &sqlx::MySqlPool,
    path: &str,
    req: &StartResultRequest,
) -> AppResult<(String, u64)> {
    validate(path, req)?;
    for attempt in 0..3 {
        let mut tx = pool.begin().await?;
        match apply(&mut tx, path, req).await {
            Ok(result) => {
                tx.commit().await?;
                return Ok(result);
            }
            Err(error) => {
                let deadlock = matches!(&error,AppError::Database(sqlx::Error::Database(db)) if db.code().as_deref()==Some("40001"));
                tx.rollback().await?;
                if !deadlock || attempt == 2 {
                    return Err(error);
                }
                tokio::time::sleep(std::time::Duration::from_millis(25 * (attempt + 1))).await;
            }
        }
    }
    unreachable!()
}

/// Returns the canonical port code/user to release only this order's Redis hold after commit.
pub async fn apply(
    tx: &mut Transaction<'_, MySql>,
    path: &str,
    req: &StartResultRequest,
) -> AppResult<(String, u64)> {
    let command = validate(path, req)?;
    let ids: Vec<(u64, Option<u64>)> = sqlx::query_as(
        "SELECT id,payment_order_id FROM charge_order WHERE order_no=? AND deleted_at IS NULL",
    )
    .bind(path)
    .fetch_all(&mut **tx)
    .await?;
    if ids.len() != 1 {
        return Err(AppError::NotFound("充电订单不存在或重复".into()));
    }
    let (cid, pid) = ids[0];
    let pid = pid.ok_or_else(conflict)?;
    // Match cancellation and payment notification lock order to avoid reversing locks.
    let pays=sqlx::query("SELECT biz_id,user_id,biz_type,status,paid_cents,refunded_cents FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(pid).fetch_all(&mut **tx).await?;
    if pays.len() != 1 {
        return Err(conflict());
    }
    let pay = &pays[0];
    let orders=sqlx::query("SELECT user_id,device_id,port_no,port_code,payment_order_id,status FROM charge_order WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(cid).fetch_all(&mut **tx).await?;
    if orders.len() != 1 {
        return Err(conflict());
    }
    let order = &orders[0];
    let uid: u64 = order.try_get("user_id")?;
    let port_code: Option<String> = order.try_get("port_code")?;
    let port_code = port_code.filter(|v| !v.is_empty()).ok_or_else(conflict)?;
    if order.try_get::<Option<u64>, _>("payment_order_id")? != Some(pid)
        || order.try_get::<String, _>("device_id")? != req.device_id
        || order.try_get::<u8, _>("port_no")? != req.port_no
        || pay.try_get::<u64, _>("biz_id")? != cid
        || pay.try_get::<u64, _>("user_id")? != uid
        || pay.try_get::<String, _>("biz_type")? != "charge"
    {
        return Err(conflict());
    }
    let receipt:Option<(String,bool,Option<u64>)>=sqlx::query_as("SELECT CAST(command_id AS CHAR CHARACTER SET utf8mb4),success,port_id FROM charge_start_receipt WHERE charge_order_id=? FOR UPDATE")
        .bind(cid).fetch_optional(&mut **tx).await?;
    if let Some((saved, success, port)) = receipt {
        if saved != command || success != req.success || port != req.port_id {
            return Err(conflict());
        }
        return Ok((port_code, uid));
    }
    if order.try_get::<String, _>("status")? != "paid"
        || pay.try_get::<String, _>("status")? != "paid"
    {
        return Err(conflict());
    }
    let mut available = pay
        .try_get::<i64, _>("paid_cents")?
        .checked_sub(pay.try_get("refunded_cents")?)
        .filter(|v| *v > 0)
        .ok_or_else(conflict)?;
    let refunds:Vec<(i64,String)>=sqlx::query_as("SELECT refund_cents,status FROM refund_record WHERE payment_order_id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(pid).fetch_all(&mut **tx).await?;
    if req.success && (!refunds.is_empty() || pay.try_get::<i64, _>("refunded_cents")? != 0) {
        return Err(conflict());
    }
    for (amount, status) in refunds {
        if amount <= 0 {
            return Err(conflict());
        }
        if status != "success" {
            available = available
                .checked_sub(amount)
                .filter(|v| *v >= 0)
                .ok_or_else(conflict)?;
        }
    }
    if req.success {
        let port_id = req.port_id.ok_or_else(conflict)?;
        let occupied=sqlx::query("SELECT port_id,device_id,port_no,charge_order_id,ended_at FROM active_port_charge WHERE port_id=? OR (device_id=? AND port_no=?) FOR UPDATE")
            .bind(port_id).bind(&req.device_id).bind(req.port_no).fetch_all(&mut **tx).await?;
        if occupied.len() > 1 {
            return Err(conflict());
        }
        if let Some(existing) = occupied.first() {
            if existing.try_get::<u64, _>("port_id")? != port_id
                || existing.try_get::<String, _>("device_id")? != req.device_id
                || existing.try_get::<u8, _>("port_no")? != req.port_no
                || (existing.try_get::<u64, _>("charge_order_id")? != cid
                    && existing
                        .try_get::<Option<chrono::NaiveDateTime>, _>("ended_at")?
                        .is_none())
            {
                return Err(conflict());
            }
            sqlx::query("UPDATE active_port_charge SET charge_order_id=?,user_id=?,started_at=UTC_TIMESTAMP(3),ended_at=NULL WHERE port_id=?")
                .bind(cid).bind(uid).bind(port_id).execute(&mut **tx).await?;
        } else {
            sqlx::query("INSERT INTO active_port_charge (port_id,device_id,port_no,charge_order_id,user_id,started_at) VALUES (?,?,?,?,?,UTC_TIMESTAMP(3))")
                .bind(port_id).bind(&req.device_id).bind(req.port_no).bind(cid).bind(uid).execute(&mut **tx).await?;
        }
        sqlx::query(
            "UPDATE charge_order SET status='charging',started_at=UTC_TIMESTAMP(3) WHERE id=?",
        )
        .bind(cid)
        .execute(&mut **tx)
        .await?;
    } else {
        sqlx::query("UPDATE charge_order SET status='failed',failure_reason=?,ended_at=UTC_TIMESTAMP(3) WHERE id=?")
            .bind(req.error.as_deref().unwrap_or("设备拒绝启动")).bind(cid).execute(&mut **tx).await?;
        if available > 0 {
            let refund_no = IdGen::new("REF").next();
            sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES (?,?,?,'charge',?,?,'设备启动失败','pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(&refund_no).bind(pid).bind(uid).bind(cid).bind(available).execute(&mut **tx).await?;
            let event = StreamEnvelope::new(
                "refund_required",
                "user",
                serde_json::json!({"refund_no":refund_no,"payment_order_id":pid,"charge_order_id":cid,"user_id":uid,"refund_cents":available}),
            );
            sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
                .bind(&event.event_id)
                .bind(common_redis::streams::REFUND_REQUIRED)
                .bind(serde_json::to_value(&event)?)
                .execute(&mut **tx)
                .await?;
        }
    }
    sqlx::query("INSERT INTO charge_start_receipt (charge_order_id,command_id,success,port_id) VALUES (?,?,?,?)")
        .bind(cid).bind(command).bind(req.success).bind(req.port_id).execute(&mut **tx).await?;
    crate::order_events::record(
        tx,
        cid,
        if req.success {
            "device_ack"
        } else {
            "start_failed"
        },
        "gateway",
        if req.success {
            "设备确认启动"
        } else {
            "设备启动失败，已申请退款"
        },
    )
    .await?;
    Ok((port_code, uid))
}

#[cfg(test)]
mod tests {
    use super::*;
    use sqlx::Executor;
    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn existing_refund_blocks_start_and_is_deducted_from_failure_refund() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let port = api_contracts::ScanPortDetail {
            device_id: format!("REF_{tag}"),
            port_no: 1,
            port_code: tag.clone(),
            port_id: tag,
            status: "idle".into(),
        };
        let (id, mut req) = paid(&mut tx, &port).await;
        sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,created_month) SELECT ?,payment_order_id,user_id,'charge',id,40,'pending',created_month FROM charge_order WHERE id=?")
            .bind(IdGen::new("REF").next()).bind(id).execute(&mut *tx).await.unwrap();
        assert!(apply(&mut tx, &req.order_no, &req).await.is_err());
        req.success = false;
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        let amounts:Vec<i64>=sqlx::query_scalar("SELECT refund_cents FROM refund_record WHERE biz_type='charge' AND biz_id=? ORDER BY refund_cents").bind(id).fetch_all(&mut *tx).await.unwrap();
        assert_eq!(amounts, vec![40, 60]);
        tx.rollback().await.unwrap();
    }
    async fn paid(
        tx: &mut Transaction<'_, MySql>,
        port: &api_contracts::ScanPortDetail,
    ) -> (u64, StartResultRequest) {
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let no = format!("START_{tag}");
        let id = crate::checkout::persist_pending(
            tx,
            123,
            &no,
            &format!("PAY_{tag}"),
            port,
            &api_contracts::QuoteResponse {
                total_cents: 100,
                electric_cents: 80,
                service_cents: 20,
            },
            chrono::Utc::now() + chrono::Duration::minutes(5),
        )
        .await
        .unwrap();
        sqlx::query("UPDATE charge_order SET status='paid' WHERE id=?")
            .bind(id)
            .execute(&mut **tx)
            .await
            .unwrap();
        sqlx::query("UPDATE payment_order SET status='paid',paid_cents=100 WHERE biz_id=? AND biz_type='charge'").bind(id).execute(&mut **tx).await.unwrap();
        (
            id,
            StartResultRequest {
                order_no: no,
                command_id: uuid::Uuid::new_v4().to_string(),
                device_id: port.device_id.clone(),
                port_no: port.port_no,
                port_id: Some(9_000_000_000 + id),
                success: true,
                error: None,
            },
        )
    }
    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn ack_is_idempotent_and_never_overwrites_another_active_order() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let port = api_contracts::ScanPortDetail {
            device_id: format!("START_{tag}"),
            port_no: 1,
            port_code: tag.clone(),
            port_id: tag,
            status: "idle".into(),
        };
        let (id, req) = paid(&mut tx, &port).await;
        assert_eq!(
            apply(&mut tx, &req.order_no, &req).await.unwrap().0,
            port.port_code
        );
        let started: chrono::NaiveDateTime =
            sqlx::query_scalar("SELECT started_at FROM charge_order WHERE id=?")
                .bind(id)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        let unchanged: chrono::NaiveDateTime =
            sqlx::query_scalar("SELECT started_at FROM charge_order WHERE id=?")
                .bind(id)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(started, unchanged);
        let (other, mut other_req) = paid(&mut tx, &port).await;
        other_req.port_id = req.port_id;
        tx.execute("SAVEPOINT busy_port").await.unwrap();
        assert!(apply(&mut tx, &other_req.order_no, &other_req)
            .await
            .is_err());
        tx.execute("ROLLBACK TO SAVEPOINT busy_port").await.unwrap();
        let occupied: u64 =
            sqlx::query_scalar("SELECT charge_order_id FROM active_port_charge WHERE port_id=?")
                .bind(req.port_id)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        assert_eq!(occupied, id);
        let status: String = sqlx::query_scalar("SELECT status FROM charge_order WHERE id=?")
            .bind(other)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(status, "paid");
        let mut wrong = req.clone();
        wrong.command_id = uuid::Uuid::new_v4().to_string();
        assert!(apply(&mut tx, &wrong.order_no, &wrong).await.is_err());
        wrong = req.clone();
        wrong.device_id.push('X');
        assert!(apply(&mut tx, &wrong.order_no, &wrong).await.is_err());
        wrong = req.clone();
        wrong.port_id = None;
        assert!(apply(&mut tx, &wrong.order_no, &wrong).await.is_err());
        sqlx::query("UPDATE charge_order SET status='completed' WHERE id=?")
            .bind(id)
            .execute(&mut *tx)
            .await
            .unwrap();
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        let status: String = sqlx::query_scalar("SELECT status FROM charge_order WHERE id=?")
            .bind(id)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
        assert_eq!(status, "completed");
        tx.rollback().await.unwrap();
    }
    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn rejected_start_records_one_refund_and_rejects_late_success() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let port = api_contracts::ScanPortDetail {
            device_id: format!("FAIL_{tag}"),
            port_no: 1,
            port_code: tag.clone(),
            port_id: tag,
            status: "idle".into(),
        };
        let (id, mut req) = paid(&mut tx, &port).await;
        req.success = false;
        req.port_id = None;
        req.error = Some("device_rejected".into());
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        apply(&mut tx, &req.order_no, &req).await.unwrap();
        let refunds: Vec<(String, i64)> = sqlx::query_as(
            "SELECT refund_no,refund_cents FROM refund_record WHERE biz_type='charge' AND biz_id=?",
        )
        .bind(id)
        .fetch_all(&mut *tx)
        .await
        .unwrap();
        assert_eq!(refunds.len(), 1);
        assert_eq!(refunds[0].1, 100);
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.payload.refund_no'))=?").bind(&refunds[0].0).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(count, 1);
        let count:i64=sqlx::query_scalar("SELECT COUNT(*) FROM charge_event_log WHERE charge_order_id=? AND event='start_failed'").bind(id).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(count, 1);
        req.success = true;
        req.port_id = Some(9_000_000_000 + id);
        assert!(apply(&mut tx, &req.order_no, &req).await.is_err());
        tx.rollback().await.unwrap();
    }
}
