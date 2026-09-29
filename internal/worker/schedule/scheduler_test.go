package schedule

import "testing"

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
