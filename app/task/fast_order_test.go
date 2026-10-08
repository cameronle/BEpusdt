package task

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func fastFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "isolated-fixture.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.Order{}, &model.Conf{}, &model.Wallet{}); err != nil {
		t.Fatal(err)
	}
	cs := []model.Conf{{K: model.PaymentMatchMode, V: "classic"}, {K: model.PaymentLookbackHour, V: "3"}, {K: model.PaymentMinAmount, V: "0.01"}, {K: model.PaymentMaxAmount, V: "99999"}}
	if err = db.Create(&cs).Error; err != nil {
		t.Fatal(err)
	}
	oldTask := log.Task
	log.Task = logrus.New()
	log.Task.SetOutput(io.Discard)
	t.Cleanup(func() { log.Task = oldTask })
	old := model.Db
	model.Db = db
	model.RefreshC()
	t.Cleanup(func() { model.Db = old; raw, _ := db.DB(); _ = raw.Close() })
	return db
}
func fastFixtureOrder() model.Order {
	created := model.Datetime(time.Now().Add(-time.Hour))
	updated := created
	zero := time.Time{}
	return model.Order{ConfirmedAt: &zero, TradeId: "isolated-fixture-1", OrderId: "isolated-merchant-1", TradeType: model.UsdtBep20, Amount: "0.01", Money: "0.01", Rate: "1", Status: model.OrderStatusWaiting, Address: "0x2222222222222222222222222222222222222222", MatchAddress: "0x2222222222222222222222222222222222222222", ExpiredAt: time.Now().Add(time.Hour), AutoTimeAt: model.AutoTimeAt{CreatedAt: &created, UpdatedAt: &updated}}
}
func TestFastReceiptKeepsConfirmationAndCanonicalTransferChecks(t *testing.T) {
	_ = fastFixtureDB(t)
	o := fastFixtureOrder()
	created := model.Datetime(time.Unix(50, 0))
	o.CreatedAt = &created
	o.ExpiredAt = time.Unix(200, 0)
	o.Status = model.OrderStatusConfirming
	o.RefBlockNum = 100
	o.RefHash = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	h := fastHeader{Number: "0x64", Timestamp: "0x64", Hash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	event := fastLog{Address: "0x55d398326f99059ff775485246999027b3197955", Topics: []string{evmTransferEvent, "0x0000000000000000000000001111111111111111111111111111111111111111", "0x0000000000000000000000002222222222222222222222222222222222222222"}, Data: "0x000000000000000000000000000000000000000000000000002386f26fc10000", BlockNumber: "0x64", BlockHash: h.Hash, TxHash: o.RefHash, Index: "0x0"}
	base := fastReceipt{Status: "0x1", BlockNumber: "0x64", BlockHash: h.Hash, TxHash: o.RefHash, Logs: []fastLog{event}}
	if err := validateFastReceipt(o, "bsc", 115, 15, base, h); err != nil {
		t.Fatalf("valid fixture refused: %v", err)
	}
	for _, name := range []string{"insufficient confirmations", "wrong network", "reverted receipt", "different block", "noncanonical header", "wrong token", "underpayment", "wrong recipient"} {
		t.Run(name, func(t *testing.T) {
			r := base
			r.Logs = append([]fastLog(nil), base.Logs...)
			header := h
			network := "bsc"
			head := int64(115)
			switch name {
			case "insufficient confirmations":
				head = 114
			case "wrong network":
				network = "polygon"
			case "reverted receipt":
				r.Status = "0x0"
			case "different block":
				r.BlockNumber = "0x65"
			case "noncanonical header":
				header.Hash = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			case "wrong token":
				r.Logs[0].Address = "0x1111111111111111111111111111111111111111"
			case "underpayment":
				r.Logs[0].Data = "0x0000000000000000000000000000000000000000000000000000000000000001"
			case "wrong recipient":
				r.Logs[0].Topics = append([]string(nil), event.Topics...)
				r.Logs[0].Topics[2] = event.Topics[1]
			}
			if validateFastReceipt(o, network, head, 15, r, header) == nil {
				t.Fatal("invalid payment accepted")
			}
		})
	}
}

func TestFastScannerPersistsMatchBeforeCursorAndDoesNotRematchReplay(t *testing.T) {
	db := fastFixtureDB(t)
	o := fastFixtureOrder()
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	tx := transfer{Network: "bsc", TradeType: model.UsdtBep20, TxHash: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BlockNum: 100, Amount: decimal.RequireFromString("0.01"), RecvAddress: o.MatchAddress, FromAddress: "0x1111111111111111111111111111111111111111", Timestamp: time.Now().Add(-time.Minute)}
	e, err := newFastEngine("bsc", filepath.Join(t.TempDir(), "progress.json"), 40, 200, 25, func(ctx context.Context, b evmBlock) (int, error) { return 1, applyFastTransfers(db, []transfer{tx}) })
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Realtime(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var stored model.Order
	if err = db.First(&stored, o.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.OrderStatusConfirming || stored.RefHash != tx.TxHash || e.Snapshot().Forward != 100 {
		t.Fatalf("advanced before durable match: %+v", stored)
	}
	if err = db.Model(&model.Order{}).Where("id = ?", o.ID).Update("status", model.OrderStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	if err = applyFastTransfers(db, []transfer{tx, tx}); err != nil {
		t.Fatal(err)
	}
	_ = db.First(&stored, o.ID).Error
	if stored.Status != model.OrderStatusSuccess {
		t.Fatal("replay changed successful payment")
	}
}
