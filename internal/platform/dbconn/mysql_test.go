package dbconn

import (
	"strings"
	"testing"
)

func TestDSN(t *testing.T) {
	dsn, err := DSN("mysql://chargepilot:p%40ss@localhost:3306/user_db", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "p@ss") || !strings.Contains(dsn, "user_db") {
		t.Fatal(dsn)
	}
	for _, bad := range []string{"", "postgres://a:b@host/db", "mysql://a:b@host", "mysql://a:b@host/db/other"} {
		if _, err := DSN(bad, false); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
