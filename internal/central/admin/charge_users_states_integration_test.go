package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestChargeUserRecentOrdersExposeIndependentStates(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with order state columns required")
	}
	db := openFinanceDB(t, "TEST_USER_DATABASE_URL")
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "recent-order-states-" + tag}
	var orders []struct {
		ID      uint64
		OrderNo string
	}
	t.Cleanup(func() {
		for _, order := range orders {
			if err := db.Exec("DELETE FROM charge_order WHERE id=? AND order_no=? AND user_id=?", order.ID, order.OrderNo, user.ID).Error; err != nil {
				t.Errorf("recent order fixture cleanup: %v", err)
			}
		}
		if err := db.Exec("DELETE FROM user WHERE id=? AND openid=?", user.ID, user.Openid).Error; err != nil {
			t.Errorf("recent user fixture cleanup: %v", err)
		}
	})
	if err := db.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i, state := range []struct{ lifecycle, payment string }{
		{"pending_payment", "pending"}, {"charging", "paid"}, {"failed", "partial_refunded"}, {"refunded", "refunded"},
	} {
		order := struct {
			ID            uint64
			OrderNo       string
			UserID        uint64
			DeviceID      string
			PortNo        uint8
			Status        string
			PaymentStatus string
			CreatedMonth  time.Time
			DeletedAt     *time.Time
		}{OrderNo: fmt.Sprintf("recent-%s-%d", tag, i), UserID: user.ID, DeviceID: "recent-state-fixture", PortNo: 1, Status: state.lifecycle, PaymentStatus: state.payment, CreatedMonth: month}
		if i == 3 {
			order.DeletedAt = &now
		}
		if err := db.Table("charge_order").Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		orders = append(orders, struct {
			ID      uint64
			OrderNo string
		}{order.ID, order.OrderNo})
	}
	store := ResourceStore{UserDB: db}
	detail, err := store.ChargeUserDetail(context.Background(), user.ID, 20)
	if err != nil || len(detail.RecentOrders) != 3 {
		t.Fatalf("recent order detail: count=%d err=%v", len(detail.RecentOrders), err)
	}
	for i, want := range []struct{ lifecycle, business, payment string }{
		{"failed", "completed", "partial_refunded"}, {"charging", "charging", "paid"}, {"pending_payment", "pending_start", "pending"},
	} {
		row := detail.RecentOrders[i]
		if row.Status != want.lifecycle || row.BusinessStatus != want.business || row.PaymentStatus != want.payment {
			t.Fatalf("recent order %d: %+v, want %s/%s/%s", i, row, want.lifecycle, want.business, want.payment)
		}
	}
	limited, err := store.ChargeUserDetail(context.Background(), user.ID, 1)
	if err != nil || len(limited.RecentOrders) != 1 || limited.RecentOrders[0].OrderID != detail.RecentOrders[0].OrderID {
		t.Fatalf("recent order limit: %+v err=%v", limited.RecentOrders, err)
	}
	router := gin.New()
	router.GET("/users/:id", (ResourceAPI{Store: store}).chargeUser)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/users/%d", user.ID), nil))
	var envelope struct {
		Data ChargeUserDetail `json:"data"`
	}
	if response.Code != http.StatusOK {
		t.Fatalf("user detail API returned %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope.Data.RecentOrders) != 3 || envelope.Data.RecentOrders[0].BusinessStatus != "completed" || envelope.Data.RecentOrders[0].PaymentStatus != "partial_refunded" || envelope.Data.RecentOrders[0].Status != "failed" {
		t.Fatalf("user detail states missing from JSON: %s err=%v", response.Body.String(), err)
	}
}
