use super::*;
use std::sync::atomic::{AtomicUsize, Ordering};

struct Handler {
    failures: usize,
    calls: AtomicUsize,
}
#[async_trait]
impl StreamHandler for Handler {
    async fn handle(&self, _: &StreamEntry) -> AppResult<()> {
        let n = self.calls.fetch_add(1, Ordering::SeqCst);
        if n < self.failures {
            Err(AppError::ServiceUnavailable("test unavailable".into()))
        } else {
            Ok(())
        }
    }
}
async fn setup() -> (RedisStream, String, StreamEnvelope) {
    let redis = RedisStream::connect(&common_config::RedisConfig::stream().unwrap())
        .await
        .unwrap();
    let name = format!("test_consumer_{}", uuid::Uuid::new_v4().simple());
    let event = StreamEnvelope::new(
        "charge_started",
        "test",
        serde_json::json!({"order_no":"fixture_only"}),
    );
    // Publish before group creation to verify startup never skips the historical event.
    redis.xadd_envelope(&name, &event).await.unwrap();
    redis.ensure_group(&name, "test-group").await.unwrap();
    (redis, name, event)
}
async fn clean(redis: &RedisStream, name: &str) {
    redis::cmd("DEL")
        .arg(name)
        .arg(format!("{name}.dlq"))
        .query_async::<i64>(&mut redis.conn())
        .await
        .unwrap();
}
const RETRIES: [Duration; 3] = [Duration::from_millis(1); 3];

#[tokio::test]
#[ignore = "requires development stream Redis"]
async fn restart_recovers_pending_and_transient_failure_retries_before_ack() {
    let (redis, name, event) = setup().await;
    let original = redis
        .xreadgroup(&name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    // Simulate a crash after delivery: deliberately do not ACK, then reconnect.
    let restarted = RedisStream::connect(&common_config::RedisConfig::stream().unwrap())
        .await
        .unwrap();
    let recovered = next_batch(&restarted, &name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    assert_eq!(recovered.len(), 1);
    assert_eq!(recovered[0].id, original[0].id);
    assert_eq!(recovered[0].envelope.event_id, event.event_id);
    let handler = Handler {
        failures: 2,
        calls: AtomicUsize::new(0),
    };
    process_entry(
        &restarted,
        "test-group",
        "stable-consumer",
        &handler,
        &recovered[0],
        &RETRIES,
    )
    .await
    .unwrap();
    let pending = restarted
        .xreadgroup_pending(&name, "test-group", "stable-consumer", 1)
        .await
        .unwrap();
    let dlq: usize = redis::cmd("XLEN")
        .arg(format!("{name}.dlq"))
        .query_async(&mut redis.conn())
        .await
        .unwrap();
    clean(&redis, &name).await;
    assert_eq!(handler.calls.load(Ordering::SeqCst), 3);
    assert!(pending.is_empty());
    assert_eq!(dlq, 0);
}

#[tokio::test]
#[ignore = "requires development stream Redis"]
async fn exhausted_retries_preserve_payload_and_atomically_ack_once() {
    let (redis, name, event) = setup().await;
    let entries = next_batch(&redis, &name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    let handler = Handler {
        failures: usize::MAX,
        calls: AtomicUsize::new(0),
    };
    assert!(redis
        .dead_letter(&entries[0], "test-group", "another-consumer", "wrong owner")
        .await
        .is_err());
    process_entry(
        &redis,
        "test-group",
        "stable-consumer",
        &handler,
        &entries[0],
        &RETRIES,
    )
    .await
    .unwrap();
    let again = redis
        .dead_letter(
            &entries[0],
            "test-group",
            "stable-consumer",
            "duplicate finalization",
        )
        .await
        .unwrap();
    let dlq: redis::streams::StreamRangeReply = redis::cmd("XRANGE")
        .arg(format!("{name}.dlq"))
        .arg("-")
        .arg("+")
        .query_async(&mut redis.conn())
        .await
        .unwrap();
    let pending = redis
        .xreadgroup_pending(&name, "test-group", "stable-consumer", 1)
        .await
        .unwrap();
    clean(&redis, &name).await;
    assert_eq!(handler.calls.load(Ordering::SeqCst), 4);
    assert!(again.is_none());
    assert!(pending.is_empty());
    assert_eq!(dlq.ids.len(), 1);
    let encoded: String =
        redis::from_redis_value(dlq.ids[0].map.get("envelope_json").unwrap()).unwrap();
    let stored: StreamEnvelope = serde_json::from_str(&encoded).unwrap();
    assert_eq!(stored.event_id, event.event_id);
    assert_eq!(stored.payload, event.payload);
    let group: String = redis::from_redis_value(dlq.ids[0].map.get("orig_group").unwrap()).unwrap();
    assert_eq!(group, "test-group");
}

#[tokio::test]
#[ignore = "requires development stream Redis"]
async fn dlq_write_failure_never_acknowledges_original_message() {
    let (redis, name, _) = setup().await;
    let entries = next_batch(&redis, &name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    let handler = Handler {
        failures: usize::MAX,
        calls: AtomicUsize::new(0),
    };
    redis::cmd("SET")
        .arg(format!("{name}.dlq"))
        .arg("wrong-key-type")
        .arg("EX")
        .arg(60)
        .query_async::<()>(&mut redis.conn())
        .await
        .unwrap();
    let result = process_entry(
        &redis,
        "test-group",
        "stable-consumer",
        &handler,
        &entries[0],
        &RETRIES,
    )
    .await;
    let pending = redis
        .xreadgroup_pending(&name, "test-group", "stable-consumer", 1)
        .await
        .unwrap();
    redis::cmd("DEL")
        .arg(format!("{name}.dlq"))
        .query_async::<i64>(&mut redis.conn())
        .await
        .unwrap();
    redis
        .dead_letter(&entries[0], "test-group", "stable-consumer", "recovered")
        .await
        .unwrap();
    let remaining = redis
        .xreadgroup_pending(&name, "test-group", "stable-consumer", 1)
        .await
        .unwrap();
    clean(&redis, &name).await;
    assert!(result.is_err());
    assert_eq!(pending.len(), 1);
    assert!(remaining.is_empty());
}

#[tokio::test]
#[ignore = "requires development stream Redis"]
async fn trimmed_pending_payload_is_reported_without_blocking_the_queue() {
    let (redis, name, _) = setup().await;
    let entries = next_batch(&redis, &name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    redis::cmd("XDEL")
        .arg(&name)
        .arg(&entries[0].id)
        .query_async::<i64>(&mut redis.conn())
        .await
        .unwrap();
    let tombstone = next_batch(&redis, &name, "test-group", "stable-consumer", 1, 1)
        .await
        .unwrap();
    let handler = Handler {
        failures: 0,
        calls: AtomicUsize::new(0),
    };
    assert_eq!(tombstone.len(), 1);
    process_entry(
        &redis,
        "test-group",
        "stable-consumer",
        &handler,
        &tombstone[0],
        &RETRIES,
    )
    .await
    .unwrap();
    let pending = redis
        .xreadgroup_pending(&name, "test-group", "stable-consumer", 1)
        .await
        .unwrap();
    let dlq: usize = redis::cmd("XLEN")
        .arg(format!("{name}.dlq"))
        .query_async(&mut redis.conn())
        .await
        .unwrap();
    clean(&redis, &name).await;
    assert_eq!(handler.calls.load(Ordering::SeqCst), 0);
    assert!(pending.is_empty());
    assert_eq!(dlq, 1);
}
