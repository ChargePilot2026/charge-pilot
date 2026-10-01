package admin

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestChargeUsersReadAndSearchPlaintextPhone(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	db := openFinanceDB(t, "TEST_USER_DATABASE_URL")
	user := struct {
		ID     uint64
		Openid string
		Phone  string
	}{Openid: "plain-phone-" + uuid.NewString(), Phone: "13987654321"}
	if err := db.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM user WHERE id = ?", user.ID) })
	store := ResourceStore{UserDB: db}
	api := ResourceAPI{Store: store}
	for _, keyword := range []string{user.Phone, "7654321"} {
		rows, total, err := store.ChargeUsers(context.Background(), PageQuery{Page: 1, PageSize: 20, Keyword: keyword})
		if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != user.ID {
			t.Fatalf("phone search %s: total=%d rows=%v err=%v", keyword, total, rows, err)
		}
		if err := api.decorateChargeUsers(t.Context(), rows); err != nil {
			t.Fatal(err)
		}
		if rows[0].Phone != user.Phone || !rows[0].PhoneBound {
			t.Fatalf("phone decoration: %+v", rows[0])
		}
	}
}
