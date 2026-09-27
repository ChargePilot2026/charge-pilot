//! 对账 / 分账能力域(P3)
//!
//! `db` 私有,handler 拿不到裸 pool。原 `api::split` 的分账状态机、
//! `order_reads::summary` 的订单计费快照查询**逐字搬移**,口径如下(勿动):
//!
//! - **幂等重放**:`settlement` 按 `(fee_calculation_id, split_template_id)`
//!   取 `ORDER BY created_month, id LIMIT 1` 的**最早**一条。命中即直接回放
//!   其 `settlement_party_amount` 并 commit,**不再校验模板、也不再重算**。
//! - **金额前置校验**:`total_cents < 0 || service_cents < 0 ||
//!   service_cents > total_cents` 一律 `ServiceUnavailable("计费金额无效，无法分账")`,
//!   发生在查模板**之前**。
//! - **模板校验**:id 不匹配 / parties 为空 / ratio_bp 合计 != 10000 → 拒绝。
//! - **mode_a** 分全额,**mode_b** 只分服务费,运营商差额
//!   `total_cents - service_cents` 事后补进 `operator` 参与方(必须存在,
//!   溢出则拒绝)。补加发生在 `split_pool` 之后、`settlement` 落库**之前**,
//!   因此库里 `split_pool_cents` 仍是纯分池值,与 `allocations` 之和可能不等。
//! - 事务边界:锁 `fee_calculation` 的 `FOR UPDATE` 与所有
//!   `settlement_party_amount` 插入在**同一个事务**里;取模板的 HTTP 调用
//!   发生在事务内(在 `FOR UPDATE` 之后),这是原实现的行为,未改。

use crate::api_types::{
    OrderSplitParty, OrderSplitResponse, SettlementDetailResponse, SplitAllocation, SplitRequest,
    SplitResponse,
};
use crate::split;
use api_contracts::orders::{OrderBilling, OrderSettlement, OrderSettlementParty};
use common_app::ServiceBase;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use sqlx::Row;

#[derive(Clone)]
pub struct SettlementService {
    base: ServiceBase,
}

impl SettlementService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    // ===== 分账 =====

    pub async fn split(&self, req: SplitRequest) -> AppResult<SplitResponse> {
        let mut tx = self.base.begin().await?;
        let calc: Option<(i64, i64, String)> = sqlx::query_as(
            "SELECT total_cents, service_cents, order_no FROM fee_calculation WHERE id = ? FOR UPDATE"
        ).bind(req.fee_calculation_id).fetch_optional(tx.executor()).await?;
        let (total_cents, service_cents, order_no) = calc.ok_or_else(|| AppError::NotFound("fee".into()))?;
        if total_cents < 0 || service_cents < 0 || service_cents > total_cents {
            return Err(AppError::ServiceUnavailable("计费金额无效，无法分账".into()));
        }
        let existing: Option<(u64, String)> = sqlx::query_as(
            "SELECT id, settlement_no FROM settlement WHERE fee_calculation_id = ? AND split_template_id = ? ORDER BY created_month, id LIMIT 1"
        ).bind(req.fee_calculation_id).bind(req.split_template_id).fetch_optional(tx.executor()).await?;
        if let Some((settlement_id, settlement_no)) = existing {
            let allocations = sqlx::query("SELECT party_id, party_code, amount_cents FROM settlement_party_amount WHERE settlement_id = ? ORDER BY id")
                .bind(settlement_id).fetch_all(tx.executor()).await?;
            let allocations: Vec<SplitAllocation> = allocations.iter().map(|row| -> AppResult<SplitAllocation> { Ok(SplitAllocation {
                party_id: sqlx::Row::try_get(row, "party_id")?,
                party_code: sqlx::Row::try_get(row, "party_code")?,
                amount_cents: sqlx::Row::try_get(row, "amount_cents")?,
            }) }).collect::<AppResult<Vec<_>>>()?;
            tx.commit().await?;
            return Ok(SplitResponse { settlement_id, settlement_no, allocations });
        }
        let template_path = api_contracts::paths::ADMIN_INTERNAL_SPLIT_TEMPLATES_GET
            .replace(":id", &req.split_template_id.to_string());
        let template: SplitTemplateView = self.base.new_client()
            .get(self.base.cfg().service_urls.admin.as_deref(), &template_path, &()).await?;
        if template.id != req.split_template_id || template.parties.is_empty()
            || template.parties.iter().map(|party| u64::from(party.ratio_bp)).sum::<u64>() != 10_000 {
            return Err(AppError::ServiceUnavailable("分账模板不存在或比例配置无效".into()));
        }
        let split_pool_cents = match template.mode.as_str() {
            "mode_a" => total_cents,
            "mode_b" => service_cents,
            _ => return Err(AppError::ServiceUnavailable("分账模板模式无效".into())),
        };
        let parties: Vec<split::Party> = template.parties.iter().map(|party| split::Party {
            id: party.id, party_code: party.party_code.clone(), ratio_bp: party.ratio_bp,
        }).collect();
        let mut allocations = split::split_pool(split_pool_cents, &parties);
        if template.mode == "mode_b" {
            let operator_share = total_cents - service_cents;
            let (_, amount) = allocations.iter_mut().find(|(party, _)| party.party_code == "operator")
                .ok_or_else(|| AppError::ServiceUnavailable("mode_b 模板必须包含 operator 参与方".into()))?;
            *amount = amount.checked_add(operator_share)
                .ok_or_else(|| AppError::ServiceUnavailable("运营商分账金额溢出".into()))?;
        }
        let settlement_no = IdGen::new("STL").next();
        let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
        let settlement_result = sqlx::query(
            "INSERT INTO settlement (settlement_no, split_template_id, mode, fee_calculation_id, order_no, total_cents, split_pool_cents, status, created_month)
         VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)"
        )
        .bind(&settlement_no).bind(req.split_template_id).bind(&template.mode).bind(req.fee_calculation_id).bind(&order_no)
        .bind(total_cents).bind(split_pool_cents).bind(&now_month)
        .execute(tx.executor()).await?;
        let settlement_id = settlement_result.last_insert_id();
        for (p, amt) in &allocations {
            let party_name = template.parties.iter().find(|party| party.id == p.id)
                .map(|party| party.party_name.as_str()).ok_or_else(|| AppError::ServiceUnavailable("分账参与方不存在".into()))?;
            sqlx::query(
                "INSERT INTO settlement_party_amount (settlement_id, party_id, party_code, party_name, ratio_bp, amount_cents)
             VALUES (?, ?, ?, ?, ?, ?)"
        )
            .bind(settlement_id).bind(p.id).bind(&p.party_code).bind(party_name).bind(p.ratio_bp).bind(amt)
            .execute(tx.executor()).await?;
        }
        tx.commit().await?;
        Ok(SplitResponse {
            settlement_id,
            settlement_no,
            allocations: allocations.iter().map(|(p, a)| SplitAllocation {
                party_id: p.id,
                party_code: p.party_code.clone(),
                amount_cents: *a,
            }).collect(),
        })
    }

    // ===== 分账结果只读 =====

    pub async fn order_split(&self, order_id: &str) -> AppResult<OrderSplitResponse> {
        let r: Option<(u64, String, String, i64, i64)> = sqlx::query_as(
            "SELECT id, settlement_no, mode, total_cents, split_pool_cents FROM settlement WHERE order_no = ? LIMIT 1"
        ).bind(order_id).fetch_optional(self.base.pool()).await?;
        let (id, no, mode, total, pool) = r.ok_or_else(|| AppError::NotFound("settlement".into()))?;
        let party_rows = sqlx::query("SELECT party_id, party_code, amount_cents, ratio_bp FROM settlement_party_amount WHERE settlement_id = ?")
            .bind(id).fetch_all(self.base.pool()).await?;
        let parties: Vec<OrderSplitParty> = party_rows.iter().map(|row| -> AppResult<OrderSplitParty> { Ok(OrderSplitParty {
            party_id: sqlx::Row::try_get::<u64, _>(row, "party_id")?,
            party_code: sqlx::Row::try_get::<String, _>(row, "party_code")?,
            amount_cents: sqlx::Row::try_get::<i64, _>(row, "amount_cents")?,
        }) }).collect::<AppResult<Vec<_>>>()?;
        Ok(OrderSplitResponse {
            settlement_id: id,
            settlement_no: no,
            mode,
            total_cents: total,
            split_pool_cents: pool,
            parties,
        })
    }

    pub async fn settlement_detail(&self, id: u64) -> AppResult<SettlementDetailResponse> {
        let r: Option<(u64, String, i64, String)> = sqlx::query_as(
            "SELECT id, settlement_no, total_cents, status FROM settlement WHERE id = ?"
        ).bind(id).fetch_optional(self.base.pool()).await?;
        let (id_, no, total, status) = r.ok_or_else(|| AppError::NotFound("settlement".into()))?;
        Ok(SettlementDetailResponse {
            id: id_, settlement_no: no, total_cents: total, status,
        })
    }

    // ===== 订单计费快照 =====

    /// billing 自有的订单计费快照,不跨 schema 查询。
    ///
    /// 一次显式事务内读完 `fee_calculation` + `settlement` + `settlement_party_amount`,
    /// 避免三段读看到互相撕裂的快照(计价已落、分账未落)。口径原样保留:
    /// `fee_calculation` 取 `ORDER BY created_at DESC, id DESC LIMIT 1`;
    /// 命中才继续读分账,`settlement` 按 `created_at, id` 升序。
    pub async fn order_summary(&self, id: u64) -> AppResult<OrderBilling> {
        let mut tx = self.base.begin().await?;
        let fee = sqlx::query("SELECT id, calculation_no, electric_cents, service_cents, total_cents FROM fee_calculation WHERE charge_order_id = ? ORDER BY created_at DESC, id DESC LIMIT 1")
            .bind(id).fetch_optional(tx.executor()).await?;
        let mut result = OrderBilling {
            calculation_no: None,
            electric_cents: None,
            service_cents: None,
            total_cents: None,
            settlements: vec![],
        };
        if let Some(fee) = fee {
            result.calculation_no = Some(fee.try_get("calculation_no")?);
            result.electric_cents = Some(fee.try_get("electric_cents")?);
            result.service_cents = Some(fee.try_get("service_cents")?);
            result.total_cents = Some(fee.try_get("total_cents")?);
            let rows = sqlx::query("SELECT id, settlement_no, mode, status, total_cents, split_pool_cents FROM settlement WHERE fee_calculation_id = ? ORDER BY created_at, id")
                .bind(fee.try_get::<u64, _>("id")?).fetch_all(tx.executor()).await?;
            for row in rows {
                let settlement_id: u64 = row.try_get("id")?;
                let party_rows = sqlx::query("SELECT party_id, party_code, party_name, ratio_bp, amount_cents, status FROM settlement_party_amount WHERE settlement_id = ? ORDER BY id")
                    .bind(settlement_id).fetch_all(tx.executor()).await?;
                let mut parties = Vec::with_capacity(party_rows.len());
                for party in party_rows {
                    parties.push(OrderSettlementParty {
                        party_id: party.try_get("party_id")?,
                        party_code: party.try_get("party_code")?,
                        party_name: party.try_get("party_name")?,
                        ratio_bp: party.try_get("ratio_bp")?,
                        amount_cents: party.try_get("amount_cents")?,
                        status: party.try_get("status")?,
                    });
                }
                result.settlements.push(OrderSettlement {
                    settlement_id,
                    settlement_no: row.try_get("settlement_no")?,
                    mode: row.try_get("mode")?,
                    status: row.try_get("status")?,
                    total_cents: row.try_get("total_cents")?,
                    split_pool_cents: row.try_get("split_pool_cents")?,
                    parties,
                });
            }
        }
        tx.commit().await?;
        Ok(result)
    }
}

#[derive(Debug, Deserialize)]
struct SplitTemplateView {
    id: u64,
    mode: String,
    parties: Vec<SplitTemplateParty>,
}

#[derive(Debug, Deserialize)]
struct SplitTemplateParty {
    id: u64,
    party_code: String,
    party_name: String,
    ratio_bp: u32,
}
