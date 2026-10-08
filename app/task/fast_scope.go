package task

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/model"
	"math/big"
	"sort"
	"strings"
	"time"
)

type fastScope struct {
	Network    string
	Contracts  map[string]model.TradeTypeConf
	Trades     map[string]model.TradeType
	Recipients map[string]bool
	Addresses  []string
	Topics     []string
}
type fastLog struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber string   `json:"blockNumber"`
	BlockHash   string   `json:"blockHash"`
	TxHash      string   `json:"transactionHash"`
	Index       string   `json:"logIndex"`
	Removed     bool     `json:"removed"`
}
type fastHeader struct {
	Number    string `json:"number"`
	Timestamp string `json:"timestamp"`
	Hash      string `json:"hash"`
}

func validFastAddress(s string) bool {
	if !strings.HasPrefix(s, "0x") || len(s) != 42 {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}
func newFastScope(network string, trades []model.TradeType, recipients []string) (*fastScope, error) {
	s := &fastScope{Network: network, Contracts: map[string]model.TradeTypeConf{}, Trades: map[string]model.TradeType{}, Recipients: map[string]bool{}}
	registry := model.GetAllTradeConfig()
	for _, trade := range trades {
		c, ok := registry[string(trade)]
		if !ok || string(c.Network) != network || c.Native || c.Contract == "" {
			return nil, errors.New("invalid token/network scope")
		}
		addr := strings.ToLower(c.Contract)
		s.Contracts[addr] = c
		s.Trades[addr] = trade
	}
	for _, addr := range recipients {
		if !validFastAddress(addr) {
			return nil, errors.New("invalid recipient scope")
		}
		s.Recipients[strings.ToLower(addr)] = true
	}
	if len(s.Contracts) == 0 || len(s.Recipients) == 0 {
		return nil, errors.New("empty token/recipient scope")
	}
	for addr := range s.Contracts {
		s.Addresses = append(s.Addresses, addr)
	}
	sort.Strings(s.Addresses)
	for addr := range s.Recipients {
		s.Topics = append(s.Topics, "0x"+strings.Repeat("0", 24)+addr[2:])
	}
	sort.Strings(s.Topics)
	return s, nil
}
func (s *fastScope) query(b evmBlock) (map[string]any, error) {
	if b.From <= 0 || b.To < b.From || b.To-b.From >= 1000 {
		return nil, errors.New("invalid scoped block range")
	}
	return map[string]any{"fromBlock": fmt.Sprintf("0x%x", b.From), "toBlock": fmt.Sprintf("0x%x", b.To), "address": s.Addresses, "topics": []any{evmTransferEvent, nil, s.Topics}}, nil
}
func (s *fastScope) Fetch(ctx context.Context, pool *fastRPC, b evmBlock) ([]transfer, error) {
	q, err := s.query(b)
	if err != nil {
		return nil, err
	}
	var events []fastLog
	if err = pool.Call(ctx, "eth_getLogs", []any{q}, &events); err != nil {
		return nil, err
	}
	headers := map[int64]fastHeader{}
	out := make([]transfer, 0, len(events))
	seen := map[string]bool{}
	for _, event := range events {
		number, err := parseFastHex(event.BlockNumber)
		index, indexErr := parseFastHex(event.Index)
		if err != nil || indexErr != nil || number < b.From || number > b.To || event.Removed || !validFastHash(event.BlockHash) || !validFastHash(event.TxHash) {
			return nil, errors.New("invalid/out-of-range transfer log")
		}
		key := fmt.Sprintf("%s:%d", event.TxHash, index)
		if seen[key] {
			continue
		}
		seen[key] = true
		t, err := s.decode(event)
		if err != nil {
			return nil, err
		}
		if t.Amount.Sign() <= 0 {
			continue
		}
		h, ok := headers[number]
		if !ok {
			if err = pool.Call(ctx, "eth_getBlockByNumber", []any{event.BlockNumber, false}, &h); err != nil {
				return nil, err
			}
			n, e := parseFastHex(h.Number)
			if e != nil || n != number {
				return nil, errors.New("RPC header returned wrong block")
			}
			headers[number] = h
		}
		if !strings.EqualFold(h.Hash, event.BlockHash) {
			return nil, errors.New("transfer/header canonical hash mismatch")
		}
		stamp, err := parseFastHex(h.Timestamp)
		if err != nil {
			return nil, err
		}
		t.Timestamp = time.Unix(stamp, 0)
		out = append(out, t)
	}
	return out, nil
}
func (s *fastScope) decode(event fastLog) (transfer, error) {
	addr := strings.ToLower(event.Address)
	c, ok := s.Contracts[addr]
	bad := errors.New("transfer outside validated token/recipient scope")
	if !ok || len(event.Topics) != 3 || event.Topics[0] != evmTransferEvent || !validFastHash(event.Topics[1]) || !validFastHash(event.Topics[2]) || event.Topics[1][2:26] != strings.Repeat("0", 24) || event.Topics[2][2:26] != strings.Repeat("0", 24) {
		return transfer{}, bad
	}
	from := "0x" + strings.ToLower(event.Topics[1][26:])
	recv := "0x" + strings.ToLower(event.Topics[2][26:])
	if !s.Recipients[recv] || !validFastHash(event.Data) {
		return transfer{}, bad
	}
	n, ok := new(big.Int).SetString(event.Data[2:], 16)
	if !ok {
		return transfer{}, bad
	}
	block, err := parseFastHex(event.BlockNumber)
	if err != nil {
		return transfer{}, err
	}
	return transfer{Network: s.Network, TxHash: strings.ToLower(event.TxHash), Amount: decimal.NewFromBigInt(n, c.Decimal), FromAddress: from, RecvAddress: recv, TradeType: s.Trades[addr], BlockNum: int(block)}, nil
}
