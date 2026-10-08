package task

import (
	"context"
	"fmt"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"os"
	"strings"
	"time"
)

func fastURLs(key string, fallback []string) []string {
	if raw := os.Getenv(key); raw != "" {
		out := []string{}
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return fallback
}
func fastCallback(network, label string, timeout time.Duration, fn func(context.Context) error) func(context.Context) {
	return func(parent context.Context) {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if err := fn(ctx); err != nil {
			log.Task.Warn(fmt.Sprintf("%s scanner-v2 %s: %v", network, label, err))
		}
	}
}
func registerFastScanner(network string, chainID int64, trade model.TradeType, offset int64) {
	if model.GetC(model.BlockOffsetConfirm) != "1" {
		panic("scanner-v2 requires enabled block confirmations")
	}
	suffix := strings.ToUpper(network)
	dir := os.Getenv("BEPUSDT_EVM_STATE_DIR")
	if dir == "" {
		dir = "/var/lib/bepusdt-shop/scanner-v2"
	}
	urls := fastURLs("BEPUSDT_RPC_"+suffix+"_URLS", []string{model.Endpoint(model.Network(network))})
	receiptDefaults := urls
	if legacy := os.Getenv("BEPUSDT_RECEIPT_RPC_" + suffix); legacy != "" {
		receiptDefaults = append([]string{legacy}, urls...)
	}
	s, err := newFastScanner(network, chainID, trade, offset, model.Db, dir, urls, fastURLs("BEPUSDT_RECEIPT_RPC_"+suffix+"_URLS", receiptDefaults))
	if err != nil {
		panic(fmt.Sprintf("%s scanner-v2 initialization failed: %v", network, err))
	}
	Register(Task{Duration: 2 * time.Second, Callback: fastCallback(network, "realtime", 12*time.Second, s.realtime)})
	Register(Task{Duration: 3 * time.Second, Callback: fastCallback(network, "confirm", 15*time.Second, s.confirm)})
	Register(Task{Duration: 5 * time.Second, Callback: fastCallback(network, "backfill", 20*time.Second, s.engine.Backfill)})
	Register(Task{Duration: 30 * time.Second, Callback: fastCallback(network, "recovery-discovery", 25*time.Second, s.discover)})
	log.Task.Info(fmt.Sprintf("%s scanner-v2 enabled: USDT-only, %d confirmations, realtime=2s, independent backfill=5s", network, offset))
}
