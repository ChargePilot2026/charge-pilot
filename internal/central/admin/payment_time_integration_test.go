package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestPaymentOrderCreatedTimeFilterIntegration(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("set disposable test database URL")
	}
	db := openFinanceDB(t, "TEST_USER_DATABASE_URL")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var baseline int64
	if err := tx.Table("payment_order").Where("deleted_at IS NULL").Count(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	// Millisecond precision and an isolated future date make both inclusive
	// boundaries observable without including unrelated current-date fixtures.
	start := time.Date(2077, 6, 15, 9, 0, 0, 123_000_000, time.UTC)
	end := start.Add(30 * time.Minute)
	prefix := "payment-time-" + uuid.NewString()
	const userID = uint64(923456789012345678)
	names := map[string]string{}
	for _, fixture := range []struct {
		name    string
		created time.Time
		paid    time.Time
		deleted bool
	}{
		{"before", start.Add(-time.Millisecond), start.Add(time.Minute), false},
		{"start", start, end.Add(24 * time.Hour), false},
		{"middle-a", start.Add(10 * time.Minute), end.Add(24 * time.Hour), false},
		{"middle-b", start.Add(20 * time.Minute), start.Add(21 * time.Minute), false},
		{"end", end, end.Add(time.Minute), false},
		{"after", end.Add(time.Millisecond), end.Add(time.Minute), false},
		{"deleted", start.Add(15 * time.Minute), start.Add(16 * time.Minute), true},
	} {
		orderNo := prefix + "-" + fixture.name
		names[fixture.name] = orderNo
		var deletedAt any
		if fixture.deleted {
			deletedAt = start
		}
		if err := tx.Exec(`INSERT INTO payment_order
			(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,created_month,created_at,paid_at,deleted_at)
			VALUES (?,'wallet_recharge',0,?,'wechat',100,100,'paid','2077-06-01',?,?,?)`,
			orderNo, userID, fixture.created, fixture.paid, deletedAt).Error; err != nil {
			t.Fatal(err)
		}
	}
	api := ResourceAPI{Store: ResourceStore{UserDB: tx}}
	router := gin.New()
	router.GET("/api/v1/admin/payment-orders", api.paymentOrders)
	requestPage := func(t *testing.T, params url.Values) Page[PaymentOrderView] {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/payment-orders?"+params.Encode(), nil))
		if response.Code != http.StatusOK {
			t.Fatalf("payment list: %d %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data Page[PaymentOrderView] `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	fromRaw := start.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	toRaw := end.In(time.FixedZone("UTC-5", -5*60*60)).Format(time.RFC3339Nano)
	t.Run("inclusive UTC boundaries and created time", func(t *testing.T) {
		page := requestPage(t, url.Values{"created_from": {fromRaw}, "created_to": {toRaw}, "page_size": {"100"}})
		want := []string{names["end"], names["middle-b"], names["middle-a"], names["start"]}
		got := make([]string, len(page.Items))
		for i, row := range page.Items {
			got[i] = row.OrderNo
			if row.UserID != userID {
				t.Fatalf("user ID lost precision: %d", row.UserID)
			}
		}
		if page.Total != 4 || !reflect.DeepEqual(got, want) {
			t.Fatalf("expected created-time range (including start/end, excluding paid-time-only and soft-deleted rows): total=%d orders=%v", page.Total, got)
		}
	})
	t.Run("filtered totals survive pagination", func(t *testing.T) {
		for _, sample := range []struct {
			page string
			want []string
		}{
			{"1", []string{names["end"], names["middle-b"]}},
			{"2", []string{names["middle-a"], names["start"]}},
			{"3", []string{}},
		} {
			page := requestPage(t, url.Values{"created_from": {fromRaw}, "created_to": {toRaw}, "page": {sample.page}, "page_size": {"2"}})
			got := make([]string, len(page.Items))
			for i, row := range page.Items {
				got[i] = row.OrderNo
			}
			if page.Total != 4 || page.PageSize != 2 || !reflect.DeepEqual(got, sample.want) {
				t.Fatalf("page %s: total=%d size=%d orders=%v", sample.page, page.Total, page.PageSize, got)
			}
		}
	})
	t.Run("empty and one-sided ranges", func(t *testing.T) {
		unbounded := requestPage(t, url.Values{"created_from": {""}, "created_to": {""}, "page_size": {"100"}})
		if unbounded.Total != baseline+6 {
			t.Fatalf("empty range excluded records or included a soft-deleted record: got %d want %d", unbounded.Total, baseline+6)
		}
		for _, sample := range []struct {
			name, from, to string
			want           int64
		}{
			{"start", fromRaw, "", 1}, {"before", fromRaw, "", 0},
			{"end", "", toRaw, 1}, {"after", "", toRaw, 0},
			{"start", fromRaw, fromRaw, 1}, {"deleted", "", "", 0},
		} {
			page := requestPage(t, url.Values{"order_no": {names[sample.name]}, "created_from": {sample.from}, "created_to": {sample.to}})
			if page.Total != sample.want || len(page.Items) != int(sample.want) {
				t.Fatalf("single/equal bounds for %s (%s, %s): total=%d rows=%d want=%d", sample.name, sample.from, sample.to, page.Total, len(page.Items), sample.want)
			}
		}
	})
}
