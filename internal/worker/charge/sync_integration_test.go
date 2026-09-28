package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestStartResultRetriesUntilCentralPersists(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable gateway database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	commandID, orderNo := uuid.NewString(), "ORD-"+uuid.NewString()
	chargeOrderID := uint64(100000000000 + time.Now().UnixNano()%900000000000)
	if _, err := db.ExecContext(ctx, `INSERT INTO charge_command
		(command_id,stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,session_id,stop_session_id,result_code,ack_at)
		VALUES (?,?,?,456,?,789,'TEST-BOARD',1,'TEST-PORT',55,TRUE,'acked','010203040506','0708090a0b0c',0,?)`, commandID, uuid.NewString(), chargeOrderID, orderNo, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM charge_command WHERE command_id = ?", commandID)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-service-token" || r.URL.Path != "/api/v1/internal/charge-orders/"+orderNo+"/start-result" {
			t.Errorf("bad internal request: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var body startResult
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CommandID != commandID || !body.Success || body.PortID != 55 {
			t.Errorf("bad body: %+v %v", body, err)
			w.WriteHeader(400)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	syncer := Synchronizer{GatewayDB: db, CentralURL: server.URL, ServiceToken: "test-service-token", CommandFilter: commandID}
	if count, err := syncer.SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("failed central call reported: %d %v", count, err)
	}
	var reported bool
	if err := db.QueryRowContext(ctx, "SELECT result_reported FROM charge_command WHERE command_id = ?", commandID).Scan(&reported); err != nil || reported {
		t.Fatalf("failed result marked reported: %v %v", reported, err)
	}
	if count, err := syncer.SyncBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry did not persist: %d %v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT result_reported FROM charge_command WHERE command_id = ?", commandID).Scan(&reported); err != nil || !reported {
		t.Fatalf("successful result not marked reported: %v %v", reported, err)
	}
}
