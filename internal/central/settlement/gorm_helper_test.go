package settlement

import (
	"database/sql"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// testGORMDB 与 charge 包测试内的同名助手保持一致；两边独立维护，
// 因本包测试与 charge 测试分属不同包，无法跨包复用内部测试符号。
func testGORMDB(t *testing.T, db *sql.DB) *gorm.DB {
	t.Helper()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}
