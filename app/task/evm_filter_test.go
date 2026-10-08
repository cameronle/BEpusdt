package task

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/conf"
)

func TestEVMLogQueryFiltersNetworkContracts(t *testing.T) {
	for _, tc := range []struct{ network, contract string }{
		{conf.Bsc, conf.UsdtBep20},
		{conf.Polygon, conf.UsdtPolygon},
	} {
		t.Run(tc.network, func(t *testing.T) {
			body, err := buildEVMLogQuery(tc.network, evmBlock{From: 100, To: 109})
			if err != nil {
				t.Fatal(err)
			}
			var req struct {
				Method string `json:"method"`
				Params []struct {
					From    string   `json:"fromBlock"`
					To      string   `json:"toBlock"`
					Address []string `json:"address"`
					Topics  []string `json:"topics"`
				} `json:"params"`
			}
			if err = json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != "eth_getLogs" || len(req.Params) != 1 {
				t.Fatalf("bad RPC envelope: %s", body)
			}
			p := req.Params[0]
			found := false
			for _, a := range p.Address {
				if a == tc.contract {
					found = true
				}
			}
			if !found {
				t.Fatalf("required token contract absent: %s", body)
			}
			if p.From != "0x64" || p.To != "0x6d" || len(p.Topics) != 1 || p.Topics[0] != evmTransferEvent {
				t.Fatalf("range/topic changed: %s", body)
			}
			for _, a := range p.Address {
				if tc.network == conf.Bsc && a == conf.UsdtPolygon || tc.network == conf.Polygon && a == conf.UsdtBep20 {
					t.Fatalf("cross-network contract: %s", body)
				}
			}
		})
	}
}

func TestEVMScanDelayBacksOffFailures(t *testing.T) {
	if got := evmScanDelay(true); got != 2*time.Second {
		t.Fatalf("successful scans need bounded pacing: %v", got)
	}
	if got := evmScanDelay(false); got != 15*time.Second {
		t.Fatalf("failed scans need backoff: %v", got)
	}
}

func TestEVMReplayBlockIsBounded(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		valid   bool
		wantErr bool
	}{
		{"", false, false}, {"126248421", true, false}, {"-1", false, true}, {"0", false, true}, {"not-a-block", false, true}, {"9223372036854775808", false, true},
	} {
		b, valid, err := parseEVMReplayBlock(tc.raw)
		if valid != tc.valid || (err != nil) != tc.wantErr {
			t.Fatalf("%q: valid=%v err=%v", tc.raw, valid, err)
		}
		if valid && (b.From != 126248421 || b.To != b.From) {
			t.Fatalf("unbounded replay: %+v", b)
		}
	}
}

func TestEVMReceiptRPCOverrideIsNetworkScoped(t *testing.T) {
	t.Setenv("BEPUSDT_RECEIPT_RPC_BSC", "https://bsc-dataseed.bnbchain.org/")
	t.Setenv("BEPUSDT_RECEIPT_RPC_POLYGON", "")
	if got := evmReceiptEndpoint("bsc", "https://bsc-rpc.publicnode.com/"); got != "https://bsc-dataseed.bnbchain.org/" {
		t.Fatalf("BSC receipt override ignored: %s", got)
	}
	if got := evmReceiptEndpoint("polygon", "https://polygon-bor-rpc.publicnode.com/"); got != "https://polygon-bor-rpc.publicnode.com/" {
		t.Fatalf("BSC override leaked across networks: %s", got)
	}
}

func TestEVMLogQueryRejectsUnknownNetwork(t *testing.T) {
	if _, err := buildEVMLogQuery("invalid-network", evmBlock{From: 100, To: 109}); err == nil {
		t.Fatal("unknown network produced unfiltered RPC query")
	}
}
