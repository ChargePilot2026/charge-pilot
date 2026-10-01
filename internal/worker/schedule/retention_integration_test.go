package schedule

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestHistoryCleanupKeepsRecentFailuresAndRunningExecutions(t *testing.T) {
	raw := os.Getenv("TEST_WORKER_DATABASE_URL")
	if raw == "" {
		t.Skip("disposable worker MySQL required")
	}
	db, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	code := "retention-" + uuid.NewString()
	defer db.Exec("DELETE FROM task_execution_log WHERE task_code=?", code)
	for _, row := range []struct {
		status   string
		age      time.Duration
		affected int
	}{
		{"success", 36 * time.Hour, 0}, {"failed", 36 * time.Hour, 0}, {"success", 36 * time.Hour, 1},
		{"failed", 31 * 24 * time.Hour, 0}, {"running", 31 * 24 * time.Hour, 0}, {"success", 12 * time.Hour, 0},
	} {
		at := time.Now().UTC().Add(-row.age)
		month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		if err := orm.Table("task_execution_log").Create(map[string]any{"task_code": code, "started_at": at, "finished_at": at, "status": row.status, "affected_rows": row.affected, "created_month": month}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := (Scheduler{DB: orm}).CleanupHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := orm.Table("task_execution_log").Where("task_code=?", code).Count(&count).Error; err != nil || count != 4 {
		t.Fatalf("retained executions=%d error=%v, want 4", count, err)
	}
}
