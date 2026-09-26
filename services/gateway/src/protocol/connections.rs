//! Live TCP writers. Session identity prevents an old connection acknowledging a new command.
use common_error::{AppError, AppResult};
use std::{collections::HashMap, sync::Arc, time::Duration};
use tokio::{
    io::AsyncWriteExt,
    net::tcp::OwnedWriteHalf,
    sync::{Mutex, RwLock},
};

#[derive(Clone)]
pub struct Session {
    pub id: String,
    pub writer: Arc<Mutex<OwnedWriteHalf>>,
}
#[derive(Clone, Default)]
pub struct Connections(Arc<RwLock<HashMap<String, Session>>>);
impl Connections {
    pub async fn register(&self, device: &str, writer: Arc<Mutex<OwnedWriteHalf>>) -> String {
        let id = uuid::Uuid::new_v4().to_string();
        self.0.write().await.insert(
            device.into(),
            Session {
                id: id.clone(),
                writer,
            },
        );
        id
    }
    pub async fn get(&self, device: &str) -> Option<Session> {
        self.0.read().await.get(device).cloned()
    }
    pub async fn remove(&self, device: &str, id: &str) {
        let mut entries = self.0.write().await;
        if entries.get(device).is_some_and(|s| s.id == id) {
            entries.remove(device);
        }
    }
}
impl Session {
    pub async fn send(&self, frame: &super::Frame) -> AppResult<()> {
        let mut data = serde_json::to_vec(frame)?;
        data.push(b'\n');
        tokio::time::timeout(Duration::from_secs(3), async {
            let mut writer = self.writer.lock().await;
            writer.write_all(&data).await
        })
        .await
        .map_err(|_| AppError::ServiceUnavailable("设备连接写入超时".into()))??;
        Ok(())
    }
}
