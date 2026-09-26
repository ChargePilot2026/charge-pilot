//! 协议层(TCP / MQTT 适配)

pub mod tcp;
pub mod connections;
pub mod mqtt;

/// 设备帧通用结构(由 adapter 标准化)
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct Frame {
    pub device_id: String,
    pub port_no: Option<u8>,
    pub msg_type: String, // heartbeat / telemetry / status / ack / cmd
    pub payload: serde_json::Value,
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