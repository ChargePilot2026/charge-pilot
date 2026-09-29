package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestScheduledTaskLeaseLogAndTrigger(t *testing.T) {
	if os.Getenv("TEST_WORKER_DATABASE_URL") == "" {
		t.Skip("disposable worker MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_WORKER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	code := "sched-" + uuid.NewString()[:8]
	defer db.ExecContext(ctx, "DELETE FROM task_execution_log WHERE task_code = ?", code)
	defer db.ExecContext(ctx, "DELETE FROM scheduled_task WHERE task_code = ?", code)
	_, err = db.ExecContext(ctx, `INSERT INTO scheduled_task(task_code,name,cron_expr,next_run_at) VALUES(?, 'test task', '@every 1s', ?)`, code, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	scheduler := Scheduler{DB: orm, Handlers: map[string]Handler{code: func(context.Context) (uint64, error) {
		runs++
		return 3, nil
	}}}
	if err := scheduler.RunDue(ctx); err != nil || runs != 1 {
		t.Fatalf("first run err=%v runs=%d", err, runs)
	}
	if err := scheduler.RunDue(ctx); err != nil || runs != 1 {
		t.Fatalf("duplicate run err=%v runs=%d", err, runs)
	}
	var status string
	var count uint64
	if err := db.QueryRowContext(ctx, "SELECT status, affected_rows FROM task_execution_log WHERE task_code = ? ORDER BY id DESC LIMIT 1", code).Scan(&status, &count); err != nil || status != "success" || count != 3 {
		t.Fatalf("execution log err=%v status=%s count=%d", err, status, count)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	API{Scheduler: scheduler, ServiceToken: "sched-secret"}.Register(router)
	path := "/api/v1/internal/scheduled-tasks/" + code + "/last-run"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	if response.Code != 401 {
		t.Fatalf("unauthorized status %d", response.Code)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Service-Token", "sched-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body struct {
		Data struct {
			Status string `json:"last_run_status"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Data.Status != "success" {
		t.Fatalf("last-run status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("POST", "/api/v1/internal/scheduled-tasks/"+code+"/trigger", strings.NewReader(`{"trigger_reason":"manual validation"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", "sched-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || runs != 2 {
		t.Fatalf("manual trigger status=%d runs=%d body=%s", response.Code, runs, response.Body.String())
	}
	if err := db.QueryRowContext(ctx, "SELECT triggered_by, trigger_reason FROM task_execution_log WHERE task_code = ? ORDER BY id DESC LIMIT 1", code).Scan(&status, &body.Data.Status); err != nil || status != "admin_api" || body.Data.Status != "manual validation" {
		t.Fatalf("manual log err=%v source=%s reason=%s", err, status, body.Data.Status)
	}
}

func TestRepeatedFailuresPauseTask(t *testing.T) {
	if os.Getenv("TEST_WORKER_DATABASE_URL") == "" {
		t.Skip("disposable worker MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_WORKER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, _ := dbconn.WrapGORM(db)
	code := "sched-" + uuid.NewString()[:8]
	defer db.ExecContext(ctx, "DELETE FROM task_execution_log WHERE task_code = ?", code)
	defer db.ExecContext(ctx, "DELETE FROM scheduled_task WHERE task_code = ?", code)
	if _, err := db.ExecContext(ctx, `INSERT INTO scheduled_task(task_code,name,cron_expr,next_run_at) VALUES(?, 'failed task', '@every 1s', UTC_TIMESTAMP(3))`, code); err != nil {
		t.Fatal(err)
	}
	scheduler := Scheduler{DB: orm, Handlers: map[string]Handler{code: func(context.Context) (uint64, error) {
		return 0, errors.New("test failure")
	}}}
	for i := 0; i < 5; i++ {
		completed, err := scheduler.Trigger(ctx, code, "retry", false)
		if !completed || err == nil {
			t.Fatalf("failure %d completed=%v err=%v", i, completed, err)
		}
	}
	var enabled bool
	var failures int
	if err := db.QueryRowContext(ctx, "SELECT enabled, consecutive_fail_count FROM scheduled_task WHERE task_code = ?", code).Scan(&enabled, &failures); err != nil || enabled || failures != 5 {
		t.Fatalf("pause err=%v enabled=%v failures=%d", err, enabled, failures)
	}
	if _, err := scheduler.Trigger(ctx, code, "blocked", false); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused task returned %v", err)
	}
}

func TestConcurrentTriggersShareLease(t *testing.T) {
	if os.Getenv("TEST_WORKER_DATABASE_URL") == "" {
		t.Skip("disposable worker MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_WORKER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, _ := dbconn.WrapGORM(db)
	code := "sched-" + uuid.NewString()[:8]
	defer db.ExecContext(ctx, "DELETE FROM task_execution_log WHERE task_code = ?", code)
	defer db.ExecContext(ctx, "DELETE FROM scheduled_task WHERE task_code = ?", code)
	if _, err := db.ExecContext(ctx, `INSERT INTO scheduled_task(task_code,name,cron_expr,next_run_at) VALUES(?, 'lease task', '@every 1s', UTC_TIMESTAMP(3))`, code); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	scheduler := Scheduler{DB: orm, Handlers: map[string]Handler{code: func(context.Context) (uint64, error) {
		close(entered)
		<-release
		return 1, nil
	}}}
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Trigger(ctx, code, "first", false)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first task did not start")
	}
	claimed, err := scheduler.Trigger(ctx, code, "second", false)
	if err != nil || claimed {
		t.Fatalf("concurrent trigger claimed=%v err=%v", claimed, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
