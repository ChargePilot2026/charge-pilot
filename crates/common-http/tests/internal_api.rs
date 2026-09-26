use axum::{
    extract::Query,
    http::{HeaderMap, StatusCode},
    routing::get,
    Json, Router,
};
use common_error::AppError;
use common_http::internal::ApiClient;
use serde_json::{json, Value};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    },
};

async fn server(app: Router) -> (String, tokio::task::JoinHandle<()>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    (base, task)
}

fn client() -> ApiClient {
    ApiClient::new(reqwest::Client::new(), Arc::new("test-token".into()))
}

#[tokio::test]
async fn post_preserves_conflicts_and_does_not_retry() {
    let attempts = Arc::new(AtomicUsize::new(0));
    let counter = attempts.clone();
    let app = Router::new().route("/provision", axum::routing::post(move |headers: HeaderMap, Json(body): Json<Value>| {
        let counter = counter.clone();
        async move {
            assert_eq!(headers["x-service-token"], "test-token");
            assert_eq!(body["device_id"], "device_01");
            counter.fetch_add(1, Ordering::SeqCst);
            (StatusCode::CONFLICT, Json(json!({"code":1006,"message":"configuration conflict","request_id":"test"})))
        }
    }));
    let (base, task) = server(app).await;
    let result = client().post::<Value,_>(Some(&base),"/provision",&json!({"device_id":"device_01"})).await;
    assert!(matches!(result, Err(AppError::Conflict(_))));
    assert_eq!(attempts.load(Ordering::SeqCst),1);
    task.abort();
}

#[tokio::test]
async fn unwraps_data_and_encodes_query_and_authentication() {
    let app = Router::new().route("/orders", get(|headers: HeaderMap, Query(query): Query<HashMap<String, String>>| async move {
        assert_eq!(headers["x-service-token"], "test-token");
        assert!(headers.contains_key("x-request-id"));
        assert_eq!(query["order_no"], "order & query=literal");
        Json(json!({"code":0,"message":"ok","request_id":"test","data":{"total":3,"items":[{"id":1}]}}))
    }));
    let (base, task) = server(app).await;
    let data: Value = client()
        .get(
            Some(&base),
            "/orders",
            &[("order_no", "order & query=literal")],
        )
        .await
        .unwrap();
    task.abort();
    assert_eq!(data["total"], 3);
    assert_eq!(data["items"][0]["id"], 1);
}

#[tokio::test]
async fn retries_server_failure_once_then_returns_real_data() {
    let attempts = Arc::new(AtomicUsize::new(0));
    let counter = attempts.clone();
    let app = Router::new().route(
        "/orders",
        get(move || {
            let counter = counter.clone();
            async move {
                if counter.fetch_add(1, Ordering::SeqCst) == 0 {
                    (StatusCode::SERVICE_UNAVAILABLE, Json(json!({})))
                } else {
                    (
                        StatusCode::OK,
                        Json(
                            json!({"code":0,"message":"ok","request_id":"test","data":{"total":1}}),
                        ),
                    )
                }
            }
        }),
    );
    let (base, task) = server(app).await;
    let data: Value = client().get(Some(&base), "/orders", &()).await.unwrap();
    task.abort();
    assert_eq!(attempts.load(Ordering::SeqCst), 2);
    assert_eq!(data["total"], 1);
}

#[tokio::test]
async fn upstream_unauthorized_is_not_operator_unauthorized() {
    let (base, task) =
        server(Router::new().route("/orders", get(|| async { StatusCode::UNAUTHORIZED }))).await;
    let error = client()
        .get::<Value, _>(Some(&base), "/orders", &())
        .await
        .unwrap_err();
    task.abort();
    assert_eq!(error.http_status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(error.code(), 5003);
}

#[tokio::test]
async fn missing_data_and_business_error_are_not_success() {
    let app = Router::new()
        .route(
            "/missing",
            get(|| async { Json(json!({"code":0,"message":"ok","request_id":"test"})) }),
        )
        .route(
            "/error",
            get(|| async { Json(json!({"code":2003,"message":"conflict","request_id":"test"})) }),
        )
        .route("/not-found", get(|| async { StatusCode::NOT_FOUND }));
    let (base, task) = server(app).await;
    assert!(matches!(
        client().get::<Value, _>(Some(&base), "/missing", &()).await,
        Err(AppError::ServiceUnavailable(_))
    ));
    assert!(matches!(
        client().get::<Value, _>(Some(&base), "/error", &()).await,
        Err(AppError::Business { code: 2003, .. })
    ));
    assert!(matches!(
        client()
            .get::<Value, _>(Some(&base), "/not-found", &())
            .await,
        Err(AppError::NotFound(_))
    ));
    task.abort();
}

#[tokio::test]
async fn pricing_conflict_and_occupied_port_remain_actionable() {
    let app=Router::new()
        .route("/pricing",get(||async{(StatusCode::CONFLICT,Json(json!({"code":1002,"message":"multiple active rules","request_id":"test"})))}))
        .route("/quote",axum::routing::post(||async{Json(json!({"code":2001,"message":"occupied","request_id":"test"}))}));
    let (base,task)=server(app).await;
    assert!(matches!(client().get::<Value,_>(Some(&base),"/pricing",&()).await,Err(AppError::Conflict(_))));
    assert!(matches!(client().post::<Value,_>(Some(&base),"/quote",&json!({})).await,Err(AppError::Business{code:2001,..})));
    task.abort();
}
