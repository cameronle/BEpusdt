package task

import (
	"context"
	"errors"
	"fmt"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"path/filepath"
	"strconv"
	"time"
)

type fastScanner struct {
	network                string
	trade                  model.TradeType
	offset                 int64
	db                     *gorm.DB
	rpc, backRPC, receipts *fastRPC
	engine                 *fastEngine
	onSuccess              func(model.Order)
}

func newFastScanner(network string, chainID int64, trade model.TradeType, offset int64, db *gorm.DB, dir string, endpoints, receipts []string) (*fastScanner, error) {
	if db == nil || offset <= 0 || (network == "bsc" && (chainID != 56 || trade != model.UsdtBep20)) || (network == "polygon" && (chainID != 137 || trade != model.UsdtPolygon)) {
		return nil, errors.New("invalid fast scanner network binding")
	}
	rpc, err := newFastRPC(chainID, endpoints, nil)
	if err != nil {
		return nil, err
	}
	receiptRPC, err := newFastRPC(chainID, receipts, nil)
	if err != nil {
		return nil, err
	}
	backRPC, err := newFastRPC(chainID, endpoints, nil)
	if err != nil {
		return nil, err
	}
	s := &fastScanner{network: network, trade: trade, offset: offset, db: db, rpc: rpc, backRPC: backRPC, receipts: receiptRPC, onSuccess: notifyOrderSuccess}
	s.engine, err = newFastEngine(network, filepath.Join(dir, network+".json"), 40, 200, 500, s.scan)
	if err == nil {
		s.engine.backScan = s.scanBackfill
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *fastScanner) currentScope() (*fastScope, error) {
	var wallets []model.Wallet
	if err := s.db.Where("trade_type = ?", s.trade).Find(&wallets).Error; err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(wallets))
	for _, w := range wallets {
		a := w.MatchAddr
		if a == "" {
			a = w.Address
		}
		addrs = append(addrs, a)
	}
	var orders []model.Order
	if err := s.db.Where("trade_type = ? and status in (?)", s.trade, receivableOrderStatuses()).Where("expired_at > ?", time.Now().Add(model.GetLookbackHour())).Find(&orders).Error; err != nil {
		return nil, err
	}
	for _, o := range orders {
		addrs = append(addrs, orderMatchAddress(o))
	}
	return newFastScope(s.network, []model.TradeType{s.trade}, addrs)
}
func (s *fastScanner) scan(ctx context.Context, b evmBlock) (int, error) {
	return s.scanWithPool(ctx, b, s.rpc)
}
func (s *fastScanner) scanBackfill(ctx context.Context, b evmBlock) (int, error) {
	return s.scanWithPool(ctx, b, s.backRPC)
}
func (s *fastScanner) scanWithPool(ctx context.Context, b evmBlock, pool *fastRPC) (int, error) {
	scope, err := s.currentScope()
	if err != nil {
		return 0, err
	}
	transfers, err := scope.Fetch(ctx, pool, b)
	if err != nil {
		return 0, err
	}
	if err = applyFastTransfers(s.db, transfers); err != nil {
		return 0, err
	}
	return len(transfers), nil
}
func (s *fastScanner) realtime(ctx context.Context) error {
	var raw string
	if err := s.rpc.Call(ctx, "eth_blockNumber", []any{}, &raw); err != nil {
		return err
	}
	head, err := parseFastHex(raw)
	if err != nil || head <= s.offset {
		return errors.New("invalid chain head")
	}
	chainBlockNum.Store(s.network, head)
	return s.engine.Realtime(ctx, head-s.offset)
}
func (s *fastScanner) header(ctx context.Context, number int64) (fastHeader, error) {
	return s.headerWithPool(ctx, number, s.rpc)
}
func (s *fastScanner) headerWithPool(ctx context.Context, number int64, pool *fastRPC) (fastHeader, error) {
	var h fastHeader
	err := pool.Call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", number), false}, &h)
	if err != nil {
		return h, err
	}
	n, err := parseFastHex(h.Number)
	if err != nil || n != number {
		return h, errors.New("RPC returned wrong header number")
	}
	return h, nil
}
func (s *fastScanner) findBefore(ctx context.Context, head, at int64) (int64, error) {
	high := head
	low := int64(1)
	step := int64(512)
	for n := head; ; {
		h, err := s.headerWithPool(ctx, n, s.backRPC)
		if err != nil {
			return 0, err
		}
		stamp, err := parseFastHex(h.Timestamp)
		if err != nil {
			return 0, err
		}
		if stamp <= at {
			low = n
			break
		}
		if n == 1 {
			return 1, nil
		}
		high = n
		n = head - step
		if n < 1 {
			n = 1
		}
		step *= 2
	}
	for low < high {
		mid := (low + high + 1) / 2
		h, err := s.headerWithPool(ctx, mid, s.backRPC)
		if err != nil {
			return 0, err
		}
		stamp, err := parseFastHex(h.Timestamp)
		if err != nil {
			return 0, err
		}
		if stamp <= at {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low, nil
}
func (s *fastScanner) discover(ctx context.Context) error {
	state := s.engine.Snapshot()
	if state.Forward <= 0 {
		return nil
	}
	var orders []model.Order
	if err := s.db.Where("trade_type = ? and status in (?)", s.trade, []int{model.OrderStatusWaiting, model.OrderStatusExpired}).Where("expired_at > ?", time.Now().Add(model.GetLookbackHour())).Order("created_at asc").Find(&orders).Error; err != nil {
		return err
	}
	for _, o := range orders {
		if o.CreatedAt == nil {
			return errors.New("order missing creation timestamp")
		}
		if state.DoneOrders[strconv.FormatInt(o.ID, 10)] > 0 {
			continue
		}
		scheduled := false
		for _, r := range state.Pending {
			for _, id := range r.OrderIDs {
				if id == o.ID {
					scheduled = true
				}
			}
		}
		if scheduled {
			continue
		}
		from, err := s.findBefore(ctx, state.Forward, o.CreatedAt.Time().Unix()-1)
		if err != nil {
			return err
		}
		if err = s.engine.AddBackfill(evmBlock{From: from, To: state.Forward}, []int64{o.ID}); err != nil {
			return err
		}
		state = s.engine.Snapshot()
	}
	return nil
}
