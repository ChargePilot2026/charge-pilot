package outbox

import (
	"database/sql"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

func testGORMDB(t *testing.T, db *sql.DB) *gorm.DB {
	t.Helper()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}
