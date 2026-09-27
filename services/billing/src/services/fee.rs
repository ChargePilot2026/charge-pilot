//! 计费能力域(P3)
//!
//! `db` 私有,handler 拿不到裸 pool。原 `charge_fee::calculate` 的
//! **幂等重放判定**与 `fee_delivery` 的**投递重试**原样搬移,见下:
//!
//! 1. `fee_receipt` 是"一次计价"的唯一凭据:`INSERT IGNORE` 后立刻
//!    `SELECT ... FOR UPDATE`,并逐字比对 `source_json`。快照一旦变化即
//!    `Conflict`,**不会**覆盖已计费订单的原始输入(资金口径不可漂移)。
//! 2. `receipt.calculation_id` 非空即视为已计费,直接回放既有 `fee_calculation`,
//!    不重复落 `fee_delivery`。
//! 3. `fee_delivery` 的成功/失败分支口径**相反**:
//!    - 成功 → `delivered=1, delivered_at=now, attempts=attempts+1`
//!    - 失败 → 只 `attempts=attempts+1, scheduled_at=now()+30s`,`delivered` 保持 0
//!    两支都在**同一个事务**里提交,失败也只是提交(保留重试),不是回滚。
//! 4. 取件用 `FOR UPDATE SKIP LOCKED` + `LIMIT 1`,多实例不抢同一行;
//!    没取到时显式 `rollback` 再返回 `false`(fire-and-forget 的析构回滚不等价)。

use crate::api_types::{CalculateResponse, FeeBreakdownResponse};
use crate::charge_fee::resolve_fee;
use api_contracts::pricing::MeteredOrder;
use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use sqlx::Row;

#[derive(Clone)]
pub struct FeeService {
    base: ServiceBase,
}

impl FeeService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    // ===== 健康检查 =====

    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }

    // ===== 计价 =====

    /// 技术规格 §8.4 的一次性计价。原实现逐字搬移,含 D10 的三步顺序:
    /// 校验计量 → 全额退款归零 → 计价。**顺序不可调换**。
    pub async fn calculate(&self, cid: u64, order_no: &str) -> AppResult<CalculateResponse> {
        if cid == 0 || order_no.is_empty() {
            return Err(AppError::BadRequest("缺少充电订单标识".into()));
        }
        let client = self.base.new_client();
        let source: MeteredOrder = client
            .get(
                self.base.cfg().service_urls.user.as_deref(),
                &api_contracts::paths::USER_INTERNAL_METERED_ORDER
                    .replace(":order_id", &cid.to_string()),
                &(),
            )
            .await?;
        if source.charge_order_id != cid || source.order_no != order_no {
            return Err(AppError::Conflict("计费订单身份不匹配".into()));
        }
        let meter = &source.meter;
        let fee = resolve_fee(&source.quote.pricing, source.started_at, meter)?;
        let snapshot = serde_json::to_value(&source)?;
        let mut tx = self.base.begin().await?;
        sqlx::query("INSERT IGNORE INTO fee_receipt (charge_order_id,source_json) VALUES (?,?)")
            .bind(cid)
            .bind(&snapshot)
            .execute(tx.executor())
            .await?;
        let receipt=sqlx::query("SELECT source_json,calculation_id,calculation_no FROM fee_receipt WHERE charge_order_id=? FOR UPDATE").bind(cid).fetch_one(tx.executor()).await?;
        if receipt.try_get::<serde_json::Value, _>("source_json")? != snapshot {
            return Err(AppError::Conflict("已计费订单的原始快照发生变化".into()));
        }
        if let Some(id) = receipt.try_get::<Option<u64>, _>("calculation_id")? {
            let no = receipt.try_get::<String, _>("calculation_no")?;
            let row=sqlx::query("SELECT electric_cents,service_cents,total_cents FROM fee_calculation WHERE id=? AND calculation_no=? AND charge_order_id=?").bind(id).bind(&no).bind(cid).fetch_one(tx.executor()).await?;
            let result = CalculateResponse {
                calculation_id: id,
                calculation_no: no,
                electric_cents: row.try_get("electric_cents")?,
                service_cents: row.try_get("service_cents")?,
                total_cents: row.try_get("total_cents")?,
            };
            tx.commit().await?;
            return Ok(result);
        }
        let no = common_db::IdGen::new("FEE").next();
        let rule = &source.quote.pricing;
        let kwh = format!("{}.{:03}", meter.charged_wh / 1000, meter.charged_wh % 1000);
        let id=sqlx::query("INSERT INTO fee_calculation (calculation_no,order_no,charge_order_id,user_id,station_id,pricing_rule_id,pricing_rule_version,charged_kwh,charged_seconds,electric_cents,service_cents,total_cents,breakdown_json,created_month) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
            .bind(&no).bind(order_no).bind(cid).bind(source.user_id).bind(rule.station_id).bind(rule.rule_id).bind(rule.version).bind(kwh).bind(meter.charged_seconds)
            .bind(fee.electric_cents).bind(fee.service_cents).bind(fee.total_cents).bind(&snapshot).bind(chrono::Utc::now().format("%Y-%m-01").to_string())
            .execute(tx.executor()).await?.last_insert_id();
        sqlx::query("UPDATE fee_receipt SET calculation_id=?,calculation_no=? WHERE charge_order_id=?")
            .bind(id)
            .bind(&no)
            .bind(cid)
            .execute(tx.executor())
            .await?;
        let delivery = api_contracts::pricing::FeeResult {
            calculation_no: no.clone(),
            source,
            electric_cents: fee.electric_cents,
            service_cents: fee.service_cents,
            total_cents: fee.total_cents,
        };
        sqlx::query("INSERT INTO fee_delivery (charge_order_id,payload_json) VALUES (?,?)")
            .bind(cid)
            .bind(serde_json::to_value(delivery)?)
            .execute(tx.executor())
            .await?;
        tx.commit().await?;
        Ok(CalculateResponse {
            calculation_id: id,
            calculation_no: no,
            electric_cents: fee.electric_cents,
            service_cents: fee.service_cents,
            total_cents: fee.total_cents,
        })
    }

    /// 费用明细只读。不开事务 —— 单表单条 SELECT。
    pub async fn fee_breakdown(&self, order_id: &str) -> AppResult<FeeBreakdownResponse> {
        let r: Option<(String, i64, i64, i64)> = sqlx::query_as(
            "SELECT calculation_no, electric_cents, service_cents, total_cents FROM fee_calculation WHERE order_no = ? LIMIT 1",
        )
        .bind(order_id)
        .fetch_optional(self.base.pool())
        .await?;
        let (no, electric, service, total) = r.ok_or_else(|| AppError::NotFound("fee".into()))?;
        Ok(FeeBreakdownResponse {
            calculation_no: no,
            electric_cents: electric,
            service_cents: service,
            total_cents: total,
        })
    }

    // ===== 计费结果投递 =====

    /// 取一条待投递的计费结果推给 user 服务。返回 `false` 表示当前无可投递项。
    ///
    /// ⚠️ 成功/失败两支**都提交事务**,失败也不回滚 —— `fee_delivery` 的行必须
    /// 带着 `attempts+1` 与推迟 30 秒的 `scheduled_at` 留下来等下一轮。
    pub async fn deliver_one(&self) -> AppResult<bool> {
        let mut tx = self.base.begin().await?;
        let row=sqlx::query("SELECT charge_order_id,payload_json FROM fee_delivery WHERE delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY scheduled_at,charge_order_id LIMIT 1 FOR UPDATE SKIP LOCKED").fetch_optional(tx.executor()).await?;
        let Some(row) = row else {
            tx.rollback().await?;
            return Ok(false);
        };
        let cid: u64 = row.try_get("charge_order_id")?;
        let payload: api_contracts::pricing::FeeResult =
            serde_json::from_value(row.try_get("payload_json")?)?;
        let client = self.base.new_client();
        let result: AppResult<serde_json::Value> = client
            .post(
                self.base.cfg().service_urls.user.as_deref(),
                &api_contracts::paths::USER_INTERNAL_FEE_RESULT.replace(":order_id", &cid.to_string()),
                &payload,
            )
            .await;
        let result = result.and_then(|v| {
            if v.get("ok").and_then(|v| v.as_bool()) == Some(true) {
                Ok(())
            } else {
                Err(AppError::Conflict("用户服务未确认实结费用".into()))
            }
        });
        if result.is_ok() {
            sqlx::query("UPDATE fee_delivery SET delivered=1,delivered_at=UTC_TIMESTAMP(3),attempts=attempts+1 WHERE charge_order_id=?").bind(cid).execute(tx.executor()).await?;
        } else {
            sqlx::query("UPDATE fee_delivery SET attempts=attempts+1,scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 30 SECOND) WHERE charge_order_id=?").bind(cid).execute(tx.executor()).await?;
        }
        tx.commit().await?;
        if let Err(error) = result {
            tracing::warn!(charge_order_id=cid,%error,"fee delivery retained for retry");
        }
        Ok(true)
    }
}
