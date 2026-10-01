package alerts

import (
	"context"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

func openAlertsDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	db, err := dbconn.Open(context.Background(), os.Getenv(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}
