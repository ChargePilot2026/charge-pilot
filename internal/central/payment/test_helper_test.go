package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 以下助手与 charge 包测试内的同名实现保持一致；两边独立维护，
// 因本包测试与 charge 测试分属不同包，无法跨包复用内部测试符号。

func testGORMDB(t *testing.T, db *sql.DB) *gorm.DB {
	t.Helper()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

func openAccountDB(t *testing.T, key string) *gorm.DB {
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

// authenticatedRouter 让每个请求都带上会话 token。
type authenticatedRouter struct {
	*gin.Engine
	token string
}

func (a *authenticatedRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Authorization", "Bearer "+a.token)
	a.Engine.ServeHTTP(w, r)
}

func callJSON(t *testing.T, router http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var request *http.Request
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request = httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
		request.Header.Set("Content-Type", "application/json")
	} else {
		request = httptest.NewRequest(method, path, nil)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	return response.Code, envelope
}

func createTwoUsers(t *testing.T, db *gorm.DB) (uint64, uint64) {
	t.Helper()
	ids := make([]uint64, 0, 2)
	for range 2 {
		openid := "qa-phone-" + uuid.NewString()[:8]
		if err := db.Exec("INSERT INTO user(openid) VALUES(?)", openid).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		db.Table("user").Where("openid = ?", openid).Pluck("id", &id)
		ids = append(ids, id)
	}
	return ids[0], ids[1]
}

// completeIntent 与 charge 包 scheme_fixture_test.go 内的同名夹具保持一致；
// 冻结完整方案与唯一套餐，供支付意图集成测试构造输入。
func completeIntent(in IntentInput, cents int64) IntentInput {
	s := pricing.Scheme{Name: "集成方案", Amount: &pricing.AmountMode{Algorithm: pricing.ModeServerEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 40}}}, Packages: []pricing.Package{{ID: 1, Name: "金额", Mode: "amount", PriceCents: cents}}}.Normalized()
	in.Rule = pricing.Rule{ID: 3, StationID: in.Port.StationID, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(in.Rule)[0]
	in.Offer = &offer
	return in
}
