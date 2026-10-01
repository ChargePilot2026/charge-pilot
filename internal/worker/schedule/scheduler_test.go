package schedule

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestTriggerRejectsUnregisteredTaskBeforeDatabaseAccess(t *testing.T) {
	for _, handlers := range []map[string]Handler{nil, {"alert_evaluate": nil}} {
		for _, force := range []bool{false, true} {
			scheduler := Scheduler{Handlers: handlers}
			completed, err := scheduler.Trigger(context.Background(), "alert_evaluate", "retired task", force)
			if completed || !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("unregistered task completed=%t err=%v force=%t", completed, err, force)
			}
		}
	}
}

func TestCronParserRejectsUnsafeTimezone(t *testing.T) {
	for _, expression := range []string{"TZ=0", "CRON_TZ=Asia/Shanghai * * * * *"} {
		if _, err := parseSpec(expression); err == nil {
			t.Fatalf("accepted timezone override %q", expression)
		}
	}
	for _, expression := range []string{"*/10 * * * * *", "0 3 * * *", "@every 1s"} {
		if _, err := parseSpec(expression); err != nil {
			t.Fatalf("rejected cron %q: %v", expression, err)
		}
	}
}
