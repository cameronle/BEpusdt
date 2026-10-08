package task

import (
	"errors"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"strings"
	"time"
)

// Commit native matching state before acknowledging scanner coverage. Successful
// and confirming orders are never re-matched; transient DB failures retain ranges.
func applyFastTransfers(db *gorm.DB, transfers []transfer) error {
	for _, t := range transfers {
		if t.TradeType != model.UsdtBep20 && t.TradeType != model.UsdtPolygon {
			continue
		}
		c := model.GetAllTradeConfig()[string(t.TradeType)]
		if string(c.Network) != t.Network || !validFastHash(t.TxHash) || !model.IsAmountValid(t.TradeType, t.Amount) {
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			var already int64
			if err := tx.Model(&model.Order{}).Where("ref_hash = ? and trade_type = ? and status in (?)", t.TxHash, t.TradeType, []int{model.OrderStatusConfirming, model.OrderStatusSuccess}).Count(&already).Error; err != nil {
				return err
			}
			if already > 0 {
				return nil
			}
			var orders []model.Order
			if err := tx.Where("trade_type = ? and status in (?)", t.TradeType, []int{model.OrderStatusWaiting, model.OrderStatusExpired}).Where("expired_at > ?", time.Now().Add(model.GetLookbackHour())).Where("match_address = ? OR (match_address = '' AND address = ?)", t.RecvAddress, t.RecvAddress).Order("created_at asc").Find(&orders).Error; err != nil {
				return err
			}
			for _, o := range orders {
				if o.CreatedAt == nil || !orderTransferMatch(o, t) {
					continue
				}
				fields := map[string]any{"status": model.OrderStatusConfirming, "ref_hash": strings.ToLower(t.TxHash), "ref_block_num": t.BlockNum, "from_address": t.FromAddress, "confirmed_at": t.Timestamp, "updated_at": time.Now()}
				if o.AddressLocked {
					rate, err := decimal.NewFromString(o.Rate)
					if err != nil {
						return errors.New("invalid locked-order rate")
					}
					fields["amount"] = t.Amount.String()
					fields["money"] = rate.Mul(t.Amount).String()
				}
				return tx.Model(&model.Order{}).Where("id = ? and status in (?)", o.ID, []int{model.OrderStatusWaiting, model.OrderStatusExpired}).Updates(fields).Error
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
