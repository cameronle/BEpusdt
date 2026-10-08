package task

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/v03413/bepusdt/app/model"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFastScannerBackfillRPCFailureDoesNotPoisonRealtime(t *testing.T) {
	db := fastFixtureDB(t)
	if err := db.Create(&model.Wallet{Address: "0x2222222222222222222222222222222222222222", TradeType: string(model.UsdtBep20), Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		w.Header().Set("Content-Type", "application/json")
		result := `"0x38"`
		switch q.Method {
		case "eth_blockNumber":
			result = `"0x73"`
		case "eth_getLogs":
			var filter map[string]string
			_ = json.Unmarshal(q.Params[0], &filter)
			n, _ := parseFastHex(filter["fromBlock"])
			if n < 50 {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"isolated historical index outage"}}`))
				return
			}
			result = `[]`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	defer server.Close()
	s, err := newFastScanner("bsc", 56, model.UsdtBep20, 15, db, t.TempDir(), []string{server.URL}, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.engine.AddBackfill(evmBlock{From: 1, To: 10}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.engine.Backfill(context.Background()); err == nil {
		t.Fatal("historical outage not exercised")
	}
	if err = s.realtime(context.Background()); err != nil {
		t.Fatalf("historical RPC cooldown poisoned realtime: %v", err)
	}
	if s.engine.Snapshot().Forward != 100 {
		t.Fatal("live confirmed watermark did not advance")
	}
}

func TestFastPolygonRegistrationReplacesLegacyQueue(t *testing.T) {
	testFastRegistration(t, "polygon", polygonInit, model.RpcEndpointPolygon)
}
func TestFastRegistrationReplacesLegacyQueueWithIndependentTimers(t *testing.T) {
	testFastRegistration(t, "bsc", bscInit, model.RpcEndpointBsc)
}
func testFastRegistration(t *testing.T, network string, initialize func(), endpointKey model.ConfKey) {
	db := fastFixtureDB(t)
	if err := db.Create(&[]model.Conf{{K: model.BlockOffsetConfirm, V: "1"}, {K: endpointKey, V: "https://rpc.invalid"}}).Error; err != nil {
		t.Fatal(err)
	}
	model.RefreshC()
	t.Setenv("BEPUSDT_EVM_SCANNER_V2", "1")
	t.Setenv("BEPUSDT_EVM_STATE_DIR", t.TempDir())
	t.Setenv("BEPUSDT_RPC_"+strings.ToUpper(network)+"_URLS", "https://rpc.invalid,https://backup.invalid")
	t.Setenv("BEPUSDT_RECEIPT_RPC_"+strings.ToUpper(network)+"_URLS", "https://receipt.invalid")
	mu.Lock()
	before := append([]Task(nil), tasks...)
	tasks = nil
	mu.Unlock()
	defer func() { mu.Lock(); tasks = before; mu.Unlock() }()
	initialize()
	mu.Lock()
	registered := append([]Task(nil), tasks...)
	mu.Unlock()
	if len(registered) != 4 {
		t.Fatalf("registered=%d", len(registered))
	}
	want := map[time.Duration]bool{2 * time.Second: true, 3 * time.Second: true, 5 * time.Second: true, 30 * time.Second: true}
	for _, task := range registered {
		if !want[task.Duration] || task.Callback == nil {
			t.Fatalf("legacy blocking queue still registered: %+v", task)
		}
		delete(want, task.Duration)
	}
	if len(want) != 0 {
		t.Fatal("missing independently scheduled task")
	}
}

func TestFastScannerInvalidReceiptDoesNotStarveFollowingOrder(t *testing.T) {
	testFastConfirmation(t, true)
}
func TestFastScannerConfirmsExactlyOnceAfterCanonicalReceipt(t *testing.T) {
	testFastConfirmation(t, false)
}
func testFastConfirmation(t *testing.T, missingFirst bool) {
	db := fastFixtureDB(t)
	o := fastFixtureOrder()
	paid := time.Now().Add(-time.Minute)
	o.Status = model.OrderStatusConfirming
	o.RefBlockNum = 100
	o.RefHash = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	o.ConfirmedAt = &paid
	if missingFirst {
		bad := o
		bad.TradeId = "isolated-missing-receipt"
		bad.RefHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := db.Create(&bad).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		result := `"0x38"`
		switch q.Method {
		case "eth_blockNumber":
			result = `"0x73"`
		case "eth_getBlockByNumber":
			result = fmt.Sprintf(`{"number":"0x64","timestamp":"0x%x","hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, paid.Unix())
		case "eth_getTransactionReceipt":
			result = `{"status":"0x1","blockNumber":"0x64","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transactionHash":"` + o.RefHash + `","logs":[{"address":"0x55d398326f99059ff775485246999027b3197955","topics":["` + evmTransferEvent + `","0x0000000000000000000000001111111111111111111111111111111111111111","0x0000000000000000000000002222222222222222222222222222222222222222"],"data":"0x000000000000000000000000000000000000000000000000002386f26fc10000","blockNumber":"0x64","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transactionHash":"` + o.RefHash + `","logIndex":"0x0"}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	defer server.Close()
	s, err := newFastScanner("bsc", 56, model.UsdtBep20, 15, db, t.TempDir(), []string{server.URL}, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.onSuccess = func(model.Order) { calls++ }
	chainBlockNum.Store("bsc", int64(115))
	defer chainBlockNum.Delete("bsc")
	for i := 0; i < 2; i++ {
		if err = s.confirm(context.Background()); err != nil && !missingFirst {
			t.Fatal(err)
		}
	}
	var stored model.Order
	_ = db.First(&stored, o.ID).Error
	if stored.Status != model.OrderStatusSuccess || calls != 1 {
		t.Fatalf("status=%d callbacks=%d", stored.Status, calls)
	}
}

func TestFastScannerDiscoversRecoveryThroughItsOwnRPC(t *testing.T) {
	db := fastFixtureDB(t)
	o := fastFixtureOrder()
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	base := o.CreatedAt.Time().Unix() - 1000
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		result := `"0x38"`
		switch q.Method {
		case "eth_blockNumber":
			result = `"0x3e8"`
		case "eth_getLogs":
			result = `[]`
		case "eth_getBlockByNumber":
			var raw string
			_ = json.Unmarshal(q.Params[0], &raw)
			n, _ := parseFastHex(raw)
			result = fmt.Sprintf(`{"number":"0x%x","timestamp":"0x%x","hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, n, base+n*2)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	defer server.Close()
	s, err := newFastScanner("bsc", 56, model.UsdtBep20, 15, db, filepath.Join(t.TempDir(), "scanner"), []string{server.URL}, []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.realtime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := s.engine.Snapshot()
	if state.Forward != 985 || len(state.Pending) != 1 || state.Pending[0].From != 499 || state.Pending[0].To != 985 || len(state.Pending[0].OrderIDs) != 1 || state.Pending[0].OrderIDs[0] != o.ID {
		t.Fatalf("order recovery missing: %+v", state)
	}
	if err = s.engine.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.engine.Snapshot().Pending) != 0 {
		t.Fatal("completed recovery not acknowledged")
	}
}
