//! 开票能力域(P3)
//!
//! `db` 私有,handler 拿不到裸 pool。
//!
//! **D6 修复的语义原样保留**:`invoice_request` 表只在 `user_db` 建,本服务连接
//! 指向 `billing_db`,且 billing_db 内无对应视图,直接查表运行时必然报
//! `Table 'billing_db.invoice_request' doesn't exist`。发票归 user 服务所有,
//! 本域只经其内部端点 `USER_INTERNAL_INVOICE_DETAIL` 取数。
//!
//! 判定口径:**发票不存在不是错误** —— `AppError::NotFound(_)` 映射为
//! `found: false, total_cents: None` 的正常响应;其余错误原样上抛。
//! 这个"吞掉 NotFound"的分支顺序不可调换。

use crate::api_types::InvoiceSettleDetailResponse;
use common_app::ServiceBase;
use common_error::{AppError, AppResult};

#[derive(Clone)]
pub struct InvoiceService {
    base: ServiceBase,
}

impl InvoiceService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    pub async fn settle_detail(&self, invoice_id: u64) -> AppResult<InvoiceSettleDetailResponse> {
        let result: AppResult<api_contracts::InvoiceDetailResponse> = self
            .base
            .new_client()
            .get(
                self.base.cfg().service_urls.user.as_deref(),
                &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_INVOICE_DETAIL, "invoice_id", &invoice_id.to_string()),
                &(),
            )
            .await;
        match result {
            Ok(detail) => Ok(InvoiceSettleDetailResponse {
                invoice_id,
                found: true,
                total_cents: Some(detail.total_cents),
            }),
            // 发票不存在不是错误,保持原 `found: false` 语义
            Err(AppError::NotFound(_)) => Ok(InvoiceSettleDetailResponse {
                invoice_id,
                found: false,
                total_cents: None,
            }),
            Err(e) => Err(e),
        }
    }
}
