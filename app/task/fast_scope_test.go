package task

import (
	"context"
	"encoding/json"
	"github.com/v03413/bepusdt/app/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFastScopeOnlyQueriesUSDTRecipientsAndMatchedHeaders(t *testing.T) {
	wallet := "0x2222222222222222222222222222222222222222"
	scope, err := newFastScope("bsc", []model.TradeType{model.UsdtBep20}, []string{wallet})
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		methods = append(methods, q.Method)
		result := `"0x38"`
		switch q.Method {
		case "eth_getLogs":
			var f map[string]any
			_ = json.Unmarshal(q.Params[0], &f)
			addresses := f["address"].([]any)
			topics := f["topics"].([]any)
			if len(addresses) != 1 || !strings.EqualFold(addresses[0].(string), "0x55d398326f99059ff775485246999027b3197955") || topics[1] != nil || topics[2].([]any)[0] != "0x0000000000000000000000002222222222222222222222222222222222222222" {
				t.Error("query not restricted to USDT and recipient")
			}
			result = `[{"address":"0x55d398326f99059ff775485246999027b3197955","topics":["` + evmTransferEvent + `","0x0000000000000000000000001111111111111111111111111111111111111111","0x0000000000000000000000002222222222222222222222222222222222222222"],"data":"0x000000000000000000000000000000000000000000000000002386f26fc10000","blockNumber":"0x64","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transactionHash":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","logIndex":"0x0","removed":false}]`
		case "eth_getBlockByNumber":
			if string(q.Params[0]) != `"0x64"` || string(q.Params[1]) != "false" {
				t.Error("queried unrelated/full block")
			}
			result = `{"number":"0x64","timestamp":"0x64","hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	defer server.Close()
	pool, err := newFastRPC(56, []string{server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ts, err := scope.Fetch(context.Background(), pool, evmBlock{From: 95, To: 110})
	if err != nil || len(ts) != 1 {
		t.Fatalf("transfers=%v err=%v", ts, err)
	}
	if ts[0].Amount.String() != "0.01" || ts[0].TradeType != model.UsdtBep20 || ts[0].RecvAddress != wallet || ts[0].Timestamp.Unix() != 100 {
		t.Fatalf("wrong decoded transfer: %+v", ts[0])
	}
	if len(methods) != 3 {
		t.Fatalf("extra full-block requests: %v", methods)
	}
	methods = nil
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		methods = append(methods, q.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	})
	ts, err = scope.Fetch(context.Background(), pool, evmBlock{From: 111, To: 125})
	if err != nil || len(ts) != 0 || len(methods) != 1 || methods[0] != "eth_getLogs" {
		t.Fatalf("empty scoped scan fetched blocks: %v %v", methods, err)
	}
}
