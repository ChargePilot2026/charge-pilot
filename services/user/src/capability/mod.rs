//! user 服务**能力域垂直切片**(P5)。
//!
//! 划分依据是**表归属 + 业务域**,与 `services.rs::UserServices` 的八个域字段一一对应:
//! - `wallet`   : 钱包余额/流水/充值/退款/风控冻结
//! - `coupon`   : 优惠券模板与用户券
//! - `order`    : 充电订单、预付下单、设备确认、计费、支付回调、订单事件
//! - `refund`   : 退款记录、审核、执行、结果回写
//! - `invoice`  : 开票申请与复核
//! - `casework` : 客服工单与设备巡检
//! - `station`  : 站点查询与用户侧报修
//! - `identity` : 登录会话与用户资料
//!
//! 每个域下的 SQL 由 `clippy::disallowed_methods` 在 crate 级 `deny`,
//! 豁免精确到文件并在文件头写明理由;生产代码禁 `json!`,
//! `serde_json::Value` 仅限方案 §三 的三类正用途(Stream 载荷 / DB JSON 列
//! 原样透出 / 动态 WHERE 拼装),各自文件级豁免已注明。

pub mod casework;
pub mod coupon;
pub mod identity;
pub mod invoice;
pub mod order;
pub mod refund;
pub mod station;
pub mod wallet;
