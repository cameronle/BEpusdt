package task

import (
	"errors"
	"github.com/v03413/bepusdt/app/model"
	"strings"
	"time"
)

type fastReceipt struct {
	Status      string    `json:"status"`
	BlockNumber string    `json:"blockNumber"`
	BlockHash   string    `json:"blockHash"`
	TxHash      string    `json:"transactionHash"`
	Logs        []fastLog `json:"logs"`
}

func validateFastReceipt(o model.Order, network string, head, offset int64, r fastReceipt, h fastHeader) error {
	bad := errors.New("receipt does not prove the quoted canonical transfer")
	number, e1 := parseFastHex(r.BlockNumber)
	headerNum, e2 := parseFastHex(h.Number)
	stamp, e3 := parseFastHex(h.Timestamp)
	if e1 != nil || e2 != nil || e3 != nil || number != int64(o.RefBlockNum) || headerNum != number || stamp <= 0 || head-number < offset || r.Status != "0x1" || !validFastHash(r.TxHash) || !validFastHash(r.BlockHash) || !strings.EqualFold(r.TxHash, o.RefHash) || !strings.EqualFold(r.BlockHash, h.Hash) || o.Status != model.OrderStatusConfirming || o.CreatedAt == nil {
		return bad
	}
	scope, err := newFastScope(network, []model.TradeType{o.TradeType}, []string{orderMatchAddress(o)})
	if err != nil {
		return bad
	}
	for _, event := range r.Logs {
		if !scope.Recipients["0x"+topicFastRecipient(event)] {
			continue
		}
		if _, ok := scope.Contracts[strings.ToLower(event.Address)]; !ok {
			continue
		}
		if event.Removed || !strings.EqualFold(event.BlockHash, r.BlockHash) || !strings.EqualFold(event.TxHash, r.TxHash) || event.BlockNumber != r.BlockNumber {
			continue
		}
		t, err := scope.decode(event)
		if err != nil {
			continue
		}
		t.Timestamp = time.Unix(stamp, 0)
		if orderTransferMatch(o, t) {
			return nil
		}
	}
	return bad
}
func topicFastRecipient(event fastLog) string {
	if len(event.Topics) != 3 || !validFastHash(event.Topics[2]) {
		return ""
	}
	return strings.ToLower(event.Topics[2][26:])
}
