package task

import (
	"context"
	"fmt"
	"github.com/v03413/bepusdt/app/model"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in read-only checks against REAL public chains, not paid acceptance.
// No production databases, merchant orders, signatures or callbacks are written.
// A disposable SQLite fixture supplies the native classic matching cache.
func TestFastLiveChainsAndReadOnlyFailover(t *testing.T) {
	if os.Getenv("BEPUSDT_RUN_LIVE_RPC_TESTS") != "1" {
		t.Skip("explicit live read-only RPC opt-in required")
	}
	for _, tc := range []struct {
		network        string
		id             int64
		trade          model.TradeType
		offset         int64
		logs, receipts []string
	}{
		{"bsc", 56, model.UsdtBep20, 15, []string{"https://bsc-rpc.publicnode.com", "https://1rpc.io/bnb"}, []string{"https://bsc-dataseed.bnbchain.org/", "https://1rpc.io/bnb"}},
		{"polygon", 137, model.UsdtPolygon, 40, []string{"https://polygon-bor-rpc.publicnode.com", "https://polygon.drpc.org"}, []string{"https://polygon.drpc.org", "https://polygon-bor-rpc.publicnode.com"}},
	} {
		t.Run(tc.network, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			p, err := newFastRPC(tc.id, tc.logs, nil)
			if err != nil {
				t.Fatal(err)
			}
			rp, err := newFastRPC(tc.id, tc.receipts, nil)
			if err != nil {
				t.Fatal(err)
			}
			var raw string
			if err = p.Call(ctx, "eth_blockNumber", []any{}, &raw); err != nil {
				t.Fatal(err)
			}
			head, err := parseFastHex(raw)
			if err != nil {
				t.Fatal(err)
			}
			cfg := model.GetAllTradeConfig()[string(tc.trade)]
			// Discover a recent public transfer on either chain. Never embed a
			// merchant wallet or a customer's transaction hash in source.
			var logs []fastLog
			if err = p.Call(ctx, "eth_getLogs", []any{map[string]any{"fromBlock": fmt.Sprintf("0x%x", head-200), "toBlock": fmt.Sprintf("0x%x", head-tc.offset), "address": []string{cfg.Contract}, "topics": []string{evmTransferEvent}}}, &logs); err != nil {
				t.Fatal(err)
			}
			if len(logs) == 0 {
				t.Fatal("no real recent USDT transfer available for receipt verification")
			}
			var receipt fastReceipt
			if err = rp.Call(ctx, "eth_getTransactionReceipt", []any{logs[0].TxHash}, &receipt); err != nil {
				t.Fatal(err)
			}
			number, err := parseFastHex(receipt.BlockNumber)
			if err != nil {
				t.Fatal(err)
			}
			recipient := ""
			for _, event := range receipt.Logs {
				if event.Address == cfg.Contract && len(event.Topics) == 3 && event.Topics[0] == evmTransferEvent {
					recipient = "0x" + topicFastRecipient(event)
					break
				}
			}
			if recipient == "" {
				t.Fatal("recent receipt has no canonical USDT recipient")
			}
			scope, err := newFastScope(tc.network, []model.TradeType{tc.trade}, []string{recipient})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			transfers, err := scope.Fetch(ctx, p, evmBlock{From: number, To: number})
			if err != nil || len(transfers) == 0 {
				t.Fatalf("real filtered transfer missing: count=%d err=%v", len(transfers), err)
			}
			var header fastHeader
			if err = p.Call(ctx, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &header); err != nil {
				t.Fatal(err)
			}
			found := false
			var expected model.Order
			for _, tr := range transfers {
				if tr.TxHash != receipt.TxHash {
					continue
				}
				created := model.Datetime(tr.Timestamp.Add(-time.Minute))
				view := model.Order{TradeType: tc.trade, Status: model.OrderStatusConfirming, Amount: tr.Amount.String(), Address: tr.RecvAddress, MatchAddress: tr.RecvAddress, RefHash: tr.TxHash, RefBlockNum: tr.BlockNum, ExpiredAt: tr.Timestamp.Add(time.Minute), AutoTimeAt: model.AutoTimeAt{CreatedAt: &created}}
				// Existing classic amount semantics are configured in the native cache below.
				old := model.Db
				db := fastFixtureDB(t)
				_ = db
				if err = validateFastReceipt(view, tc.network, head, tc.offset, receipt, header); err != nil {
					t.Fatal(err)
				}
				model.Db = old
				expected = view
				found = true
				break
			}
			if !found {
				t.Fatal("receipt transaction not found in canonical scoped logs")
			}
			t.Logf("REAL %s: chain_id=%d head=%d block=%d canonical filtered fetch=%s receipt_verified=true (not a new merchant payment)", tc.network, tc.id, head, number, time.Since(started))
			var failures atomic.Int32
			broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				failures.Add(1)
				http.Error(w, "isolated injected HTTP outage", 503)
			}))
			defer broken.Close()
			failover, err := newFastRPC(tc.id, append([]string{broken.URL}, tc.logs[1]), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = failover.Call(ctx, "eth_blockNumber", []any{}, &raw); err != nil {
				t.Fatal(err)
			}
			if failures.Load() != 1 {
				t.Fatal("fault endpoint not exercised")
			}
			probe, err := newFastScope(tc.network, []model.TradeType{tc.trade}, []string{recipient})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = probe.Fetch(ctx, failover, evmBlock{From: head - 100, To: head - tc.offset}); err != nil {
				t.Fatal(err)
			}
			t.Logf("REAL %s backup %s: primary outage injected locally, live head + exact recipient-filtered logs succeeded", tc.network, tc.logs[1])
			recovered, err := scope.Fetch(ctx, failover, evmBlock{From: number, To: number})
			if err != nil || len(recovered) == 0 {
				t.Fatalf("backup historical token/header fetch: %v", err)
			}
			var backupReceipt fastReceipt
			if err = failover.Call(ctx, "eth_getTransactionReceipt", []any{receipt.TxHash}, &backupReceipt); err != nil {
				t.Fatalf("backup historical receipt: %v", err)
			}
			var backupHeader fastHeader
			if err = failover.Call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", number), false}, &backupHeader); err != nil {
				t.Fatal(err)
			}
			if err = validateFastReceipt(expected, tc.network, head, tc.offset, backupReceipt, backupHeader); err != nil {
				t.Fatalf("backup canonical receipt rejected: %v", err)
			}
			t.Logf("REAL %s backup: historical recipient log + timestamp header + canonical transfer receipt all verified", tc.network)
		})
	}
}
