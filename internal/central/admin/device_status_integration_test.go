package admin

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDeviceOperationRespectsScopeAndWritesAudit(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	prefix := "operation-" + uuid.NewString()
	actor := uint64(913001)
	var sites []uint64
	for _, suffix := range []string{"own", "other"} {
		name := prefix + suffix
		if err := db.Exec("INSERT INTO station(name,longitude,latitude,status) VALUES(?,120,30,'active')", name).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		db.Table("station").Where("name=?", name).Pluck("id", &id)
		sites = append(sites, id)
	}
	own, other := prefix+"1", prefix+"2"
	t.Cleanup(func() {
		db.Exec("DELETE FROM admin_data_scope WHERE admin_user_id=?", actor)
		db.Exec("DELETE FROM audit_log WHERE actor_id=?", actor)
		db.Exec("DELETE FROM device_meta WHERE device_id IN (?,?)", own, other)
		db.Exec("DELETE FROM station WHERE id IN ?", sites)
	})
	if err := db.Exec("INSERT INTO device_meta(device_id,station_id,status) VALUES(?,?,'enabled'),(?,?,'enabled')", own, sites[0], other, sites[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES(?,'station',?,?)", actor, sites[0], actor).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("admin_profile", Profile{ID: actor, Role: "customer_ops", Permissions: []string{"device.operate"}})
	})
	r.PUT("/devices/:id/status", (ResourceAPI{Store: ResourceStore{AdminDB: db}}).setDeviceStatus)
	for _, tc := range []struct {
		id, status string
		code       int
	}{{own, "disabled", 200}, {other, "disabled", 404}, {own, "enabled", 200}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("PUT", "/devices/"+tc.id+"/status", bytes.NewBufferString(`{"status":"`+tc.status+`"}`)))
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.id, w.Code, w.Body.String())
		}
	}
	var audits int64
	db.Table("audit_log").Where("actor_id=? AND action='device.status'", actor).Count(&audits)
	if audits != 2 {
		t.Fatalf("status audit count %d", audits)
	}
	var status string
	db.Table("device_meta").Where("device_id=?", other).Pluck("status", &status)
	if status != "enabled" {
		t.Fatal("outside-scope device changed")
	}
}
