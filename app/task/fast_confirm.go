package task

import (
	"context"
	"errors"
	"fmt"
	"github.com/v03413/bepusdt/app/model"
	"time"
)

func (s *fastScanner) confirm(ctx context.Context) error {
	orders := getConfirmingOrders([]model.TradeType{s.trade})
	if len(orders) == 0 {
		return nil
	}
	var raw string
	if err := s.rpc.Call(ctx, "eth_blockNumber", []any{}, &raw); err != nil {
		return err
	}
	head, err := parseFastHex(raw)
	if err != nil {
		return err
	}
	var failures []error
	for _, o := range orders {
		if head-int64(o.RefBlockNum) < s.offset {
			continue
		}
		var receipt fastReceipt
		if err = s.receipts.Call(ctx, "eth_getTransactionReceipt", []any{o.RefHash}, &receipt); err != nil {
			failures = append(failures, err)
			continue
		}
		h, err := s.header(ctx, int64(o.RefBlockNum))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err = validateFastReceipt(o, s.network, head, s.offset, receipt, h); err != nil {
			failures = append(failures, fmt.Errorf("%s canonical receipt validation: %w", s.network, err))
			continue
		}
		result := s.db.Model(&model.Order{}).Where("id = ? and status = ? and ref_hash = ?", o.ID, model.OrderStatusConfirming, o.RefHash).Updates(map[string]any{"status": model.OrderStatusSuccess, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			o.Status = model.OrderStatusSuccess
			s.onSuccess(o)
		}
	}
	return errors.Join(failures...)
}
