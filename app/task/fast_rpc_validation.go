package task

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

func validFastHash(s string) bool {
	if !strings.HasPrefix(s, "0x") || len(s) != 66 {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}
func validateFastResult(method string, raw json.RawMessage) error {
	bad := errors.New("invalid RPC result semantics")
	switch method {
	case "eth_blockNumber":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return bad
		}
		number, err := parseFastHex(value)
		if err != nil || number <= 0 {
			return bad
		}
	case "eth_getBlockByNumber":
		var b struct {
			Number    string `json:"number"`
			Timestamp string `json:"timestamp"`
			Hash      string `json:"hash"`
		}
		if json.Unmarshal(raw, &b) != nil {
			return bad
		}
		number, e1 := parseFastHex(b.Number)
		stamp, e2 := parseFastHex(b.Timestamp)
		if e1 != nil || e2 != nil || number <= 0 || stamp <= 0 || !validFastHash(b.Hash) {
			return bad
		}
	case "eth_getLogs":
		var logs []json.RawMessage
		if json.Unmarshal(raw, &logs) != nil || logs == nil {
			return bad
		}
	case "eth_getTransactionReceipt":
		var r struct {
			Status      string            `json:"status"`
			BlockNumber string            `json:"blockNumber"`
			BlockHash   string            `json:"blockHash"`
			Hash        string            `json:"transactionHash"`
			Logs        []json.RawMessage `json:"logs"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return bad
		}
		number, err := parseFastHex(r.BlockNumber)
		if err != nil || number <= 0 || !validFastHash(r.BlockHash) || !validFastHash(r.Hash) || (r.Status != "0x0" && r.Status != "0x1") || r.Logs == nil {
			return bad
		}
	default:
		return bad
	}
	return nil
}
