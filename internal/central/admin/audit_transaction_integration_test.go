package admin

import (
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestAuditFailureRollsBackCentralBusinessChange(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central MySQL required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	name := "audit-rollback-" + uuid.NewString()
	t.Cleanup(func() { db.Table("station").Where("name=?", name).Delete(nil) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/test", nil)
	c.Set("admin_profile", Profile{ID: 901001, Username: "audit-test"})
	forced := errors.New("audit storage unavailable")
	callback := "test:reject-audit:" + uuid.NewString()
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(callback) })
	var entries []auditEntry
	err := (ResourceAPI{}).auditedTransaction(c, db, &entries, func(tx *gorm.DB) error {
		if err := tx.Table("station").Create(map[string]any{"name": name, "longitude": 120, "latitude": 30, "status": "active"}).Error; err != nil {
			return err
		}
		entries = []auditEntry{{action: "create", target: "station", id: 1}}
		return nil
	})
	if !errors.Is(err, forced) {
		t.Fatalf("error=%v", err)
	}
	var count int64
	if err := db.Table("station").Where("name=?", name).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("business committed without audit: count=%d error=%v", count, err)
	}
}
