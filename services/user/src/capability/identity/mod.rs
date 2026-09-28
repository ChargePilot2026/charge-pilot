//! **identity 域** — 登录会话(刷新令牌 / 单次使用)、用户资料、手机号绑定。
//!
//! `repository_sql.rs` 是搬迁前的 `login.rs`(微信 openid → user_id 解析),
//! 本域 repository 层,SQL 豁免按文件给出并写明理由。

pub mod profile;
pub mod login;
pub mod session;
