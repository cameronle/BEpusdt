package task

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/model"
)

// The environment carries the actual scalar read from the deployed gateway.
// These are isolated database fixtures, not live payment or fulfillment tests.
func TestRequestedUSDTStepAllocatesDistinctAmountsPerNetwork(t *testing.T) {
	step := os.Getenv("BEPUSDT_QA_ATOM_USDT")
	if step == "" {
		step = "0.0001"
	}
	db := fastFixtureDB(t)
	if err := db.Create(&model.Conf{K: model.AtomUSDT, V: step}).Error; err != nil {
		t.Fatal(err)
	}
	model.RefreshC()
	want := []string{"0.01", "0.0101", "0.0102"}
	address := "0x1111111111111111111111111111111111111111"
	for _, trade := range []model.TradeType{model.UsdtBep20, model.UsdtPolygon} {
		t.Run(string(trade), func(t *testing.T) {
			wallets := []model.Wallet{{Address: address, MatchAddr: address, TradeType: string(trade), Status: model.WaStatusEnable}}
			for i, expected := range want {
				wallet, amount, err := model.CalcTradeAmount(wallets, decimal.NewFromInt(1), model.OrderParams{Money: decimal.RequireFromString("0.01"), TradeType: trade})
				if err != nil {
					t.Fatal(err)
				}
				if amount != expected {
					t.Fatalf("step %s: slot %d = %s, want %s", step, i, amount, expected)
				}
				order := fastFixtureOrder()
				order.TradeType = trade
				order.TradeId = fmt.Sprintf("isolated-step-%s-%d", trade, i)
				order.OrderId = order.TradeId
				order.RefHash = order.TradeId
				order.Address = wallet.Address
				order.MatchAddress = wallet.MatchAddr
				order.Amount = amount
				if i == 1 {
					order.Status = model.OrderStatusConfirming
				}
				if err = db.Create(&order).Error; err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("actual native allocator: %s", want)
		})
	}
}

func TestFourDecimalUSDTStrictMatchingPreservesOldOrderAmounts(t *testing.T) {
	db := fastFixtureDB(t)
	legacy := fastFixtureOrder()
	legacy.Amount = "0.02"
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Conf{K: model.AtomUSDT, V: "0.0001"}).Error; err != nil {
		t.Fatal(err)
	}
	model.RefreshC()
	var stored model.Order
	if err := db.First(&stored, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Amount != "0.02" {
		t.Fatal("setting new atom changed an existing order quote")
	}
	quoted := fastFixtureOrder()
	quoted.Amount = "0.0101"
	transfer := transfer{Network: "bsc", TradeType: model.UsdtBep20, RecvAddress: quoted.MatchAddress, Timestamp: time.Now().Add(-time.Minute)}
	for _, amount := range []string{"0.01", "0.0101", "0.0102", "0.01011"} {
		transfer.Amount = decimal.RequireFromString(amount)
		if got := orderTransferMatch(quoted, transfer); got != (amount == "0.0101") {
			t.Fatalf("strict match for %s = %v", amount, got)
		}
	}
	transfer.TradeType = model.UsdtPolygon
	transfer.Amount = decimal.RequireFromString("0.0101")
	if orderTransferMatch(quoted, transfer) {
		t.Fatal("same amount on a different network matched")
	}
}
