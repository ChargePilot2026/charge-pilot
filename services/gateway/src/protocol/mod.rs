//! 已实现的 TCP/JSON 协议层；厂商协议与 MQTT 适配尚未接入。

pub mod tcp;
pub mod connections;

/// 设备原始遥测帧载荷。
///
/// 方案 §三 例外清单第 2 类明确允许 `gateway_db.raw_frame_log` 的设备原始帧
/// 保持不透明。单独起一个别名、并在**定义处**豁免，是为了让所有引用点
/// （含 `Frame` 字段与 `handle_frame`）都通过这个名字而不是直接写
/// `serde_json::Value` —— `clippy::disallowed_types` 对**类型引用节点**
/// 报诊断，字段级 `#[allow]` 抑制不到 derive 宏展开与跨文件引用。
#[allow(clippy::disallowed_types)]
pub type RawFramePayload = serde_json::Value;

/// 设备帧通用结构(由 adapter 标准化)
///
/// 解析在 `protocol::tcp::handle_frame` 的帧边界完成,随即转成
/// `services::device::Measurement` 具名结构,不向下游传播。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct Frame {
    pub device_id: String,
    pub port_no: Option<u8>,
    pub msg_type: String, // heartbeat / telemetry / status / ack / cmd
    pub payload: RawFramePayload,
    pub ts: chrono::DateTime<chrono::Utc>,
}

/// 厂商适配器 trait(简化;生产可放每家硬件厂独立子模块)
pub trait Adapter: Send + Sync {
    fn vendor_id(&self) -> &'static str;
    fn parse_frame(&self, buf: &[u8]) -> Result<Frame, ParseError>;
    fn serialize_frame(&self, frame: &Frame) -> Vec<u8>;
}

#[derive(Debug, thiserror::Error)]
pub enum ParseError {
    #[error("buffer too short")]
    TooShort,
    #[error("invalid header")]
    InvalidHeader,
    #[error("checksum mismatch")]
    Checksum,
    #[error("unsupported protocol version")]
    Version,
    #[error("io error: {0}")]
    Io(String),
}
