//! Run against a disposable Redis database with TEST_REDIS_URL set:
//! cargo test -p common-stream --test registration -- --ignored

use async_trait::async_trait;
use common_config::RedisConfig;
use common_error::{AppError, AppResult};
use common_redis::{RedisCache, RedisStream, StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use std::time::Duration;
use tokio::sync::mpsc;

struct Capture(mpsc::UnboundedSender<String>);

#[async_trait]
impl StreamHandler for Capture {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        self.0.send(entry.envelope.event_id.clone()).unwrap();
        Ok(())
    }
}

async fn check_registration(stream_name: &'static str) {
    let mut cfg = RedisConfig {
        url: std::env::var("TEST_REDIS_URL")
            .expect("set TEST_REDIS_URL to a disposable Redis database"),
        pool_size: 1,
        is_stream: false,
    };
    // This client only prepares and removes test fixtures.
    let fixtures = RedisCache::connect(&cfg).await.unwrap();
    cfg.is_stream = true;
    let stream = RedisStream::connect(&cfg).await.unwrap();
    let cg = ConsumerGroup::new(stream.clone());
    let (tx, mut rx) = mpsc::unbounded_channel();

    fixtures.del(stream_name).await.unwrap();
    fixtures
        .set_ex(stream_name, &"wrong-type fixture", 60)
        .await
        .unwrap();
    let failed = cg
        .register(
            stream_name,
            "test-group",
            "test-1",
            Box::new(Capture(tx.clone())),
            1,
            50,
        )
        .await;
    assert!(
        matches!(failed, Err(AppError::Redis(_))),
        "initialization errors must reach the caller"
    );
    fixtures.del(stream_name).await.unwrap();

    let first = cg
        .register(
            stream_name,
            "test-group",
            "test-1",
            Box::new(Capture(tx.clone())),
            1,
            50,
        )
        .await
        .unwrap();
    // Reusing an existing group must succeed as it does on service restart.
    let second = cg
        .register(
            stream_name,
            "test-group",
            "test-2",
            Box::new(Capture(tx)),
            1,
            50,
        )
        .await
        .unwrap();

    let event = StreamEnvelope::new("test", "registration-test", serde_json::json!({}));
    stream.xadd_envelope(stream_name, &event).await.unwrap();
    let received = tokio::time::timeout(Duration::from_secs(5), rx.recv()).await;
    first.abort();
    second.abort();
    let _ = first.await;
    let _ = second.await;
    fixtures.del(stream_name).await.unwrap();
    assert_eq!(received.unwrap().unwrap(), event.event_id);
}

#[tokio::test]
#[ignore = "requires TEST_REDIS_URL pointing to a disposable Redis database"]
async fn register_on_current_thread_runtime() {
    check_registration("test:common-stream:register:current-thread").await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
#[ignore = "requires TEST_REDIS_URL pointing to a disposable Redis database"]
async fn register_on_multi_thread_runtime() {
    check_registration("test:common-stream:register:multi-thread").await;
}
