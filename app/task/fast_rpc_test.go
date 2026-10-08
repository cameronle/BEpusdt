package task

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFastRPCRejectsMalformedSuccessfulResult(t *testing.T) {
	for _, tc := range []struct{ method, bad, good string }{
		{"eth_blockNumber", `"0xZZ"`, `"0x64"`},
		{"eth_getBlockByNumber", `{}`, `{"number":"0x64","timestamp":"0x64","hash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{"eth_getLogs", `{}`, `[]`},
		{"eth_getTransactionReceipt", `{"status":"0x9"}`, `{"status":"0x1","blockNumber":"0x64","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transactionHash":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","logs":[]}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			makeHandler := func(result string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					var q struct {
						Method string `json:"method"`
					}
					_ = json.NewDecoder(r.Body).Decode(&q)
					reply := result
					if q.Method == "eth_chainId" {
						reply = `"0x38"`
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + reply + `}`))
				}
			}
			bad := httptest.NewServer(makeHandler(tc.bad))
			defer bad.Close()
			good := httptest.NewServer(makeHandler(tc.good))
			defer good.Close()
			p, err := newFastRPC(56, []string{bad.URL, good.URL}, good.Client())
			if err != nil {
				t.Fatal(err)
			}
			var raw json.RawMessage
			params := []any{}
			if tc.method == "eth_getLogs" {
				params = []any{map[string]any{"fromBlock": "0x1", "toBlock": "0x2"}}
			}
			if err = p.Call(context.Background(), tc.method, params, &raw); err != nil || string(raw) != tc.good {
				t.Fatalf("invalid result accepted: %s err=%v", raw, err)
			}
		})
	}
}

func TestFastRPCMissingReceiptDoesNotPoisonOtherReceipts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		reply := `"0x38"`
		if q.Method == "eth_getTransactionReceipt" {
			reply = `null`
			if q.Params[0] == "known-fixture-hash" {
				reply = `{"status":"0x1","blockNumber":"0x64","blockHash":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","transactionHash":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","logs":[]}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + reply + `}`))
	}))
	defer server.Close()
	p, err := newFastRPC(56, []string{server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err = p.Call(context.Background(), "eth_getTransactionReceipt", []any{"missing-fixture-hash"}, &raw); err == nil {
		t.Fatal("null receipt accepted")
	}
	if err = p.Call(context.Background(), "eth_getTransactionReceipt", []any{"known-fixture-hash"}, &raw); err != nil {
		t.Fatalf("one missing hash poisoned other receipts: %v", err)
	}
}

func TestFastRPCAdaptsToProviderLogRangeLimit(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "eth_chainId" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x38"}`))
			return
		}
		queries.Add(1)
		from, _ := parseFastHex(q.Params[0]["fromBlock"].(string))
		to, _ := parseFastHex(q.Params[0]["toBlock"].(string))
		if to-from+1 > 50 {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"eth_getLogs is limited to 0 - 50 blocks range"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	}))
	defer server.Close()
	p, err := newFastRPC(56, []string{server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var logs []json.RawMessage
		if err = p.Call(context.Background(), "eth_getLogs", []any{map[string]any{"fromBlock": "0x1", "toBlock": "0x96", "address": []string{"0x55d398326f99059ff775485246999027b3197955"}, "topics": []any{evmTransferEvent}}}, &logs); err != nil || logs == nil {
			t.Fatalf("bounded log pagination failed: %v", err)
		}
	}
	if queries.Load() != 7 {
		t.Fatalf("provider limit not cached or coverage wrong: queries=%d", queries.Load())
	}
}

func TestFastRPCFailoverAndCooldown(t *testing.T) {
	var badCalls atomic.Int32
	handler := func(bad bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var q struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			w.Header().Set("Content-Type", "application/json")
			if q.Method == "eth_chainId" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x38"}`))
				return
			}
			if bad {
				badCalls.Add(1)
				http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x64"}`))
		}
	}
	bad := httptest.NewServer(handler(true))
	defer bad.Close()
	good := httptest.NewServer(handler(false))
	defer good.Close()
	pool, err := newFastRPC(56, []string{bad.URL, good.URL}, good.Client())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var head string
		if err = pool.Call(context.Background(), "eth_blockNumber", []any{}, &head); err != nil || head != "0x64" {
			t.Fatalf("head=%q err=%v", head, err)
		}
	}
	if badCalls.Load() != 1 {
		t.Fatalf("failed endpoint was hammered: %d", badCalls.Load())
	}
}
