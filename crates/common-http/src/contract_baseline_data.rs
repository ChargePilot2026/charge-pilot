//! 当前对外契约基线快照 —— 由 `scripts/gen_contract_baseline.py` 从各服务
//! `main.rs` 的实际注册生成(P1a)。**P2 契约重写与 P6 变更清单以此为对照。**
//!
//! ⚠️ 已知错误行为见 `contract_baseline::EXCLUDED`,不在此快照内。

use super::contract_baseline::{BaselineRoute, ContractBaseline};
use std::collections::BTreeMap;

/// 改造前的实际注册路由
pub const BASELINE: &[BaselineRoute] = &[
    BaselineRoute { method: "GET", path: "/api/v1/admin/alert-rules", handler_hint: "admin:alert::rules_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/alert-rules", handler_hint: "admin:alert::rules_create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/alert-rules/:id", handler_hint: "admin:alert::rules_delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/alert-rules/:id", handler_hint: "admin:alert::rules_get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/alert-rules/:id", handler_hint: "admin:alert::rules_update" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/alert-subscriptions", handler_hint: "admin:alert::subs_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/alert-subscriptions", handler_hint: "admin:alert::subs_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/alerts", handler_hint: "admin:alert::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/alerts/:id/ack", handler_hint: "admin:alert::ack" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/announcements", handler_hint: "admin:api::announcements::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/announcements", handler_hint: "admin:api::announcements::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/announcements/:id", handler_hint: "admin:api::announcements::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/announcements/:id", handler_hint: "admin:api::announcements::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/announcements/:id", handler_hint: "admin:api::announcements::update" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/auth/logout", handler_hint: "admin:auth::logout" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/invoices", handler_hint: "admin:billing::invoices" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/invoices/:id/approve", handler_hint: "admin:billing::invoice_approve" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/invoices/:id/reject", handler_hint: "admin:billing::invoice_reject" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/reconcile-logs", handler_hint: "admin:billing::reconcile_logs" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/refunds", handler_hint: "admin:billing::refunds" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/refunds/:id/approve", handler_hint: "admin:billing::refund_approve" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/refunds/:id/reject", handler_hint: "admin:billing::refund_reject" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/refunds/:id/retry", handler_hint: "admin:billing::refund_retry" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/settlements", handler_hint: "admin:billing::settlements" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/wallet-risks", handler_hint: "admin:billing::wallet_risks" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/wallet-risks/:request_id/release", handler_hint: "admin:billing::wallet_risk_release" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/wallet-risks/:request_id/review", handler_hint: "admin:billing::wallet_risk_review" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/billing/withdraw", handler_hint: "admin:billing::withdraw_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/withdraw", handler_hint: "admin:billing::withdraw_create" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/billing/withdraw/:id/review", handler_hint: "admin:billing::withdraw_review" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/coupons", handler_hint: "admin:api::coupons::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/coupons", handler_hint: "admin:api::coupons::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/coupons/:id", handler_hint: "admin:api::coupons::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/coupons/:id", handler_hint: "admin:api::coupons::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/coupons/:id", handler_hint: "admin:api::coupons::update" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/coupons/:id/grants", handler_hint: "admin:api::coupons::grant" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/coupons/:id/stats", handler_hint: "admin:api::coupons::stats" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/customer-service", handler_hint: "admin:api::customer_service::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/customer-service", handler_hint: "admin:api::customer_service::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/customer-service/:id", handler_hint: "admin:api::customer_service::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/customer-service/:id", handler_hint: "admin:api::customer_service::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/customer-service/:id", handler_hint: "admin:api::customer_service::update" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/dashboard", handler_hint: "admin:api::dashboard::get" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/device-fault-reports", handler_hint: "admin:api::casework::fault_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/device-fault-reports/:id/dispatch", handler_hint: "admin:api::casework::fault_dispatch" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/device-fault-reports/:id/history", handler_hint: "admin:api::casework::fault_history" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/device-fault-reports/:id/resolve", handler_hint: "admin:api::casework::fault_resolve" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/device-imports", handler_hint: "admin:api::device_import::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/device-imports", handler_hint: "admin:api::device_import::create" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/device-imports/:id/retry", handler_hint: "admin:api::device_import::retry" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/devices", handler_hint: "admin:api::devices::list" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/devices/:id", handler_hint: "admin:api::devices::get" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/devices/:id/orders", handler_hint: "admin:api::devices::orders" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/export", handler_hint: "admin:api::export::create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/export/tasks", handler_hint: "admin:api::export::tasks" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/export/tasks/:task_id", handler_hint: "admin:api::export::task" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/export/tasks/:task_id/download", handler_hint: "admin:api::export::download" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/feedback", handler_hint: "admin:api::casework::feedback_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/feedback/:id/reply", handler_hint: "admin:api::casework::feedback_reply" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/membership", handler_hint: "admin:api::membership::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/membership", handler_hint: "admin:api::membership::create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/orders", handler_hint: "admin:api::orders::list" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/orders/:id", handler_hint: "admin:api::orders::get" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/orders/:id/refunds", handler_hint: "admin:billing::refund_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/orders/:id/timeline", handler_hint: "admin:api::orders::timeline" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/ota/packages", handler_hint: "admin:ota::packages_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/ota/packages", handler_hint: "admin:ota::packages_create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/ota/packages/:id", handler_hint: "admin:ota::packages_delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/ota/packages/:id", handler_hint: "admin:ota::packages_get" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/ota/schedules", handler_hint: "admin:ota::schedules_list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/ota/schedules", handler_hint: "admin:ota::schedules_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/ota/schedules/:id", handler_hint: "admin:ota::schedules_get" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/ota/schedules/:id", handler_hint: "admin:ota::schedules_trigger" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/permissions", handler_hint: "admin:api::roles::permissions" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/risk-config", handler_hint: "admin:alert::risk_config_get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/risk-config", handler_hint: "admin:alert::risk_config_put" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/roles", handler_hint: "admin:api::roles::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/roles", handler_hint: "admin:api::roles::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/roles/:id", handler_hint: "admin:api::roles::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/roles/:id", handler_hint: "admin:api::roles::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/roles/:id", handler_hint: "admin:api::roles::update" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/settings/charge-rules", handler_hint: "admin:api::settings::charge_rules" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/settings/charge-rules", handler_hint: "admin:api::settings::charge_rule_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/settings/ota", handler_hint: "admin:api::settings::ota_get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/settings/ota", handler_hint: "admin:api::settings::ota_put" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/settings/pricing-templates", handler_hint: "admin:api::settings::pricing_templates" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/settings/pricing-templates", handler_hint: "admin:api::settings::pricing_template_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/settings/split-templates", handler_hint: "admin:api::settings::split_templates" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/settings/split-templates", handler_hint: "admin:api::settings::split_template_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/settings/split-templates/:id/parties", handler_hint: "admin:api::settings::split_parties" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/settings/split-templates/:id/parties", handler_hint: "admin:api::settings::split_party_create" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/stations", handler_hint: "admin:api::stations::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/stations", handler_hint: "admin:api::stations::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/stations/:id", handler_hint: "admin:api::stations::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/stations/:id", handler_hint: "admin:api::stations::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/stations/:id", handler_hint: "admin:api::stations::update" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/users", handler_hint: "admin:api::users::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/users", handler_hint: "admin:api::users::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/users/:id", handler_hint: "admin:api::users::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/users/:id", handler_hint: "admin:api::users::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/users/:id", handler_hint: "admin:api::users::update" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/users/:id/reset-password", handler_hint: "admin:api::users::reset_password" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/webhooks", handler_hint: "admin:webhook::list" },
    BaselineRoute { method: "POST", path: "/api/v1/admin/webhooks", handler_hint: "admin:webhook::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/admin/webhooks/:id", handler_hint: "admin:webhook::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/webhooks/:id", handler_hint: "admin:webhook::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/webhooks/:id", handler_hint: "admin:webhook::update" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/webhooks/:id/deliveries", handler_hint: "admin:webhook::deliveries" },
    BaselineRoute { method: "GET", path: "/api/v1/admin/whitelabel", handler_hint: "admin:api::whitelabel::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/admin/whitelabel", handler_hint: "admin:api::whitelabel::put" },
    BaselineRoute { method: "GET", path: "/api/v1/health", handler_hint: "admin:api::health" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/alerts", handler_hint: "admin:api::internal::alerts_active" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/announcements/active", handler_hint: "admin:api::internal::announcements_active" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/announcements/expire", handler_hint: "admin:api::internal::announcements_expire" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/customer-service/entry", handler_hint: "admin:api::internal::customer_service_entry" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/pricing", handler_hint: "admin:api::pricing_reads::device" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/:id/reboot", handler_hint: "admin:api::internal::device_reboot" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/pricing-rules/:id", handler_hint: "admin:api::internal::pricing_rule_get" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/split-templates/:id", handler_hint: "admin:api::internal::split_template_get" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/stations/:station_id", handler_hint: "admin:api::station_reads::detail" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/stations/nearby", handler_hint: "admin:api::station_reads::nearby" },
    BaselineRoute { method: "POST", path: "/api/v1/public/auth/login", handler_hint: "admin:auth::login" },
    BaselineRoute { method: "POST", path: "/api/v1/public/auth/refresh", handler_hint: "admin:auth::refresh" },
    BaselineRoute { method: "GET", path: "/api/v1/health", handler_hint: "billing:api::health" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/calculate", handler_hint: "billing:api::calculate" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/invoices/:invoice_id/settle-detail", handler_hint: "billing:api::invoice_settle_detail" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders/:order_id/billing-summary", handler_hint: "billing:order_reads::summary" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders/:order_id/fee-breakdown", handler_hint: "billing:api::fee_breakdown" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders/:order_id/split", handler_hint: "billing:api::order_split" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/quote", handler_hint: "billing:api::quote" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refunds/:refund_id/calc", handler_hint: "billing:api::refund_calc" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/settlements/:settlement_id", handler_hint: "billing:api::settlement_detail" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/split", handler_hint: "billing:api::split" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/withdraw-requests", handler_hint: "billing:api::withdraw_create" },
    BaselineRoute { method: "GET", path: "/api/v1/health", handler_hint: "gateway:api::health" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/charge-orders/stop", handler_hint: "gateway:charge_stop::request" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/device-sessions/cleanup-idle", handler_hint: "gateway:api::cleanup_idle_device_sessions" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/device/register", handler_hint: "gateway:registration::register" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id", handler_hint: "gateway:api::device_get" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/:id/backfill", handler_hint: "gateway:api::device_backfill" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/:id/command", handler_hint: "gateway:api::device_command" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/curve", handler_hint: "gateway:api::device_curve" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/:id/firmware-push", handler_hint: "gateway:api::device_firmware_push" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/historical-curve", handler_hint: "gateway:api::device_historical_curve" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/orders", handler_hint: "gateway:api::device_orders" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/ports", handler_hint: "gateway:api::device_ports" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/:id/reboot", handler_hint: "gateway:api::device_reboot" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:id/snapshot", handler_hint: "gateway:api::device_snapshot" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/devices/provision", handler_hint: "gateway:provision::provision" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/scan/port", handler_hint: "gateway:api::scan_port" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/scan/resolve", handler_hint: "gateway:api::scan_resolve" },
    BaselineRoute { method: "GET", path: "/api/v1/health", handler_hint: "user:api::health" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/charge-orders/:order_id/end-result", handler_hint: "user:charge_end::receive" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/charge-orders/:order_id/fee-result", handler_hint: "user:charge_fee::receive" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/charge-orders/:order_id/metered", handler_hint: "user:charge_end::metered_order" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/charge-orders/:order_id/start-result", handler_hint: "user:payment::start_result" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/charge-orders/charging", handler_hint: "user:orders::charging_orders_for_snapshots" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/coupons", handler_hint: "user:coupon_admin::list" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/coupons", handler_hint: "user:coupon_admin::create" },
    BaselineRoute { method: "DELETE", path: "/api/v1/internal/coupons/:id", handler_hint: "user:coupon_admin::delete" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/coupons/:id", handler_hint: "user:coupon_admin::get" },
    BaselineRoute { method: "PUT", path: "/api/v1/internal/coupons/:id", handler_hint: "user:coupon_admin::update" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/coupons/:id/grants", handler_hint: "user:coupon_admin::grant" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/coupons/stats", handler_hint: "user:coupon_admin::stats" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/dashboard/metrics", handler_hint: "user:dashboard::metrics" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/device-fault-reports", handler_hint: "user:casework::fault_list" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/device-fault-reports/:id/dispatch", handler_hint: "user:casework::fault_dispatch" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/device-fault-reports/:id/history", handler_hint: "user:casework::fault_history" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/device-fault-reports/:id/resolve", handler_hint: "user:casework::fault_resolve" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/devices/:device_id/orders", handler_hint: "user:orders::device_orders" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/feedback", handler_hint: "user:casework::feedback_list" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/feedback/:id/reply", handler_hint: "user:casework::feedback_reply" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/invoices/:invoice_id", handler_hint: "user:invoice::internal_detail" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/invoices/:invoice_id", handler_hint: "user:invoice::internal_review" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders", handler_hint: "user:orders::list" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders/:order_id", handler_hint: "user:orders::detail" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/orders/:order_id/refunds", handler_hint: "user:manual_refund::create" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/orders/:order_id/timeline", handler_hint: "user:order_events::timeline" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/payment-orders/:payment_order_id", handler_hint: "user:payment::detail" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/refund-records", handler_hint: "user:refund::list" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refund-records/:refund_id/approve", handler_hint: "user:refund_review::receive" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refund-records/:refund_id/reject", handler_hint: "user:refund_review::reject_receive" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refund-records/:refund_id/result", handler_hint: "user:refund::result" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refund-records/claim", handler_hint: "user:refund::claim" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/refund-records/execution", handler_hint: "user:refund_execution::prepare" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/refunds/:refund_id", handler_hint: "user:refund::detail" },
    BaselineRoute { method: "GET", path: "/api/v1/internal/wallet-risks", handler_hint: "user:wallet_refund::risk_list" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/wallet-risks/:request_id/release", handler_hint: "user:wallet_risk_release::receive" },
    BaselineRoute { method: "POST", path: "/api/v1/internal/wallet-risks/:request_id/review", handler_hint: "user:wallet_refund::review_handler" },
    BaselineRoute { method: "POST", path: "/api/v1/public/auth/login", handler_hint: "user:api::login" },
    BaselineRoute { method: "POST", path: "/api/v1/public/auth/logout", handler_hint: "user:session::logout" },
    BaselineRoute { method: "POST", path: "/api/v1/public/auth/refresh", handler_hint: "user:session::refresh" },
    BaselineRoute { method: "POST", path: "/api/v1/public/payment/wechat/callback", handler_hint: "user:payment::wechat_callback" },
    BaselineRoute { method: "POST", path: "/api/v1/public/refund/wechat/callback", handler_hint: "user:refund_result::callback" },
    BaselineRoute { method: "GET", path: "/api/v1/user/announcement/list", handler_hint: "user:api::announcement_list" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/:order_id", handler_hint: "user:orders::user_detail" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/:order_id/curve", handler_hint: "user:api::charge_historical_curve" },
    BaselineRoute { method: "POST", path: "/api/v1/user/charge/:order_id/feedback", handler_hint: "user:api::charge_feedback" },
    BaselineRoute { method: "POST", path: "/api/v1/user/charge/:order_id/prepay", handler_hint: "user:prepay::resume" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/history", handler_hint: "user:orders::user_history" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/ongoing", handler_hint: "user:api::charge_ongoing" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/ongoing/curve", handler_hint: "user:api::charge_curve" },
    BaselineRoute { method: "GET", path: "/api/v1/user/charge/ongoing/snapshot", handler_hint: "user:api::charge_snapshot" },
    BaselineRoute { method: "POST", path: "/api/v1/user/charge/stop", handler_hint: "user:api::charge_stop" },
    BaselineRoute { method: "GET", path: "/api/v1/user/coupon/my", handler_hint: "user:coupon::my" },
    BaselineRoute { method: "POST", path: "/api/v1/user/coupon/preview", handler_hint: "user:coupon::preview" },
    BaselineRoute { method: "POST", path: "/api/v1/user/customer-service/entry", handler_hint: "user:api::customer_service_entry" },
    BaselineRoute { method: "GET", path: "/api/v1/user/device/fault-reports", handler_hint: "user:station::my_fault_reports" },
    BaselineRoute { method: "GET", path: "/api/v1/user/device/fault-reports/:id/history", handler_hint: "user:station::my_fault_history" },
    BaselineRoute { method: "POST", path: "/api/v1/user/device/report-fault", handler_hint: "user:station::report_fault" },
    BaselineRoute { method: "POST", path: "/api/v1/user/invoice/apply", handler_hint: "user:invoice::apply" },
    BaselineRoute { method: "GET", path: "/api/v1/user/invoice/my", handler_hint: "user:invoice::my" },
    BaselineRoute { method: "POST", path: "/api/v1/user/phone/bind", handler_hint: "user:api::phone_bind" },
    BaselineRoute { method: "POST", path: "/api/v1/user/phone/unbind", handler_hint: "user:api::phone_unbind" },
    BaselineRoute { method: "GET", path: "/api/v1/user/profile", handler_hint: "user:profile::get" },
    BaselineRoute { method: "POST", path: "/api/v1/user/scan/cancel", handler_hint: "user:api::scan_cancel" },
    BaselineRoute { method: "POST", path: "/api/v1/user/scan/port", handler_hint: "user:api::scan_port" },
    BaselineRoute { method: "POST", path: "/api/v1/user/scan/quote", handler_hint: "user:api::scan_quote" },
    BaselineRoute { method: "POST", path: "/api/v1/user/scan/resolve", handler_hint: "user:api::scan_resolve" },
    BaselineRoute { method: "POST", path: "/api/v1/user/scan/start", handler_hint: "user:api::scan_start" },
    BaselineRoute { method: "GET", path: "/api/v1/user/station/:station_id", handler_hint: "user:station::detail" },
    BaselineRoute { method: "GET", path: "/api/v1/user/station/nearby", handler_hint: "user:station::nearby" },
    BaselineRoute { method: "GET", path: "/api/v1/user/wallet/balance", handler_hint: "user:wallet_reads::balance" },
    BaselineRoute { method: "POST", path: "/api/v1/user/wallet/recharge", handler_hint: "user:wallet::recharge" },
    BaselineRoute { method: "GET", path: "/api/v1/user/wallet/recharges", handler_hint: "user:wallet_recharge::list" },
    BaselineRoute { method: "POST", path: "/api/v1/user/wallet/refund", handler_hint: "user:wallet::refund" },
    BaselineRoute { method: "GET", path: "/api/v1/user/wallet/refunds", handler_hint: "user:wallet_refund::list" },
    BaselineRoute { method: "GET", path: "/api/v1/user/wallet/txns", handler_hint: "user:wallet_reads::txns" },
];

/// 构造完整基线
pub fn baseline() -> ContractBaseline {
    ContractBaseline {
        routes: BASELINE.to_vec(),
        excluded: super::contract_baseline::EXCLUDED
            .iter()
            .map(|(id, note)| (*id, *note))
            .collect::<BTreeMap<_, _>>(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn baseline_is_not_empty() {
        let b = baseline();
        assert!(b.route_count() > 100, "基线应覆盖全部已注册路由,实际 {}", b.route_count());
    }

    /// 同一服务内 (method, path) 唯一 —— 跨服务重复是合法的(如各自 /health)
    #[test]
    fn no_duplicate_within_a_service() {
        use std::collections::HashSet;
        let b = baseline();
        let mut seen = HashSet::new();
        for r in &b.routes {
            let service = r.handler_hint.split(':').next().unwrap_or("?");
            assert!(
                seen.insert((service, r.method, r.path)),
                "服务内重复注册: {service} {} {}", r.method, r.path
            );
        }
    }

    /// 4 个 HTTP 服务各自探活,worker 的探活挂在 /health(不在本基线路径表内)
    #[test]
    fn health_is_registered_by_every_http_service() {
        let b = baseline();
        let services: std::collections::HashSet<&str> = b
            .routes
            .iter()
            .filter(|r| r.path == "/api/v1/health" && r.method == "GET")
            .map(|r| r.handler_hint.split(':').next().unwrap_or("?"))
            .collect();
        assert_eq!(
            services.len(),
            4,
            "4 个 HTTP 服务各自探活,实际 {services:?}"
        );
    }
}