package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

type fastLogLimit struct{ Max int64 }

func (e *fastLogLimit) Error() string { return fmt.Sprintf("RPC log range limit %d", e.Max) }

var fastRangePattern = regexp.MustCompile(`(?i)limited to 0\s*-\s*([0-9]+) blocks range`)

func parseFastLogLimit(raw json.RawMessage) error {
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return errors.New("RPC returned error")
	}
	if e.Code == -32602 || e.Code == -32000 || e.Code == -32005 {
		parts := fastRangePattern.FindStringSubmatch(e.Message)
		if len(parts) == 2 {
			n, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil && n > 0 && n <= 1000 {
				return &fastLogLimit{Max: n}
			}
		}
	}
	return errors.New("RPC returned error")
}
func (p *fastRPC) requestAdaptive(ctx context.Context, endpoint, method string, params any) (json.RawMessage, error) {
	if method != "eth_getLogs" {
		return p.request(ctx, endpoint, method, params)
	}
	xs, ok := params.([]any)
	if !ok || len(xs) != 1 {
		return nil, errors.New("invalid RPC log parameters")
	}
	filter, ok := xs[0].(map[string]any)
	if !ok {
		return nil, errors.New("invalid RPC log filter")
	}
	a, aok := filter["fromBlock"].(string)
	z, zok := filter["toBlock"].(string)
	from, e1 := parseFastHex(a)
	to, e2 := parseFastHex(z)
	if !aok || !zok || e1 != nil || e2 != nil || from <= 0 || to < from || to-from >= 1000 {
		return nil, errors.New("invalid bounded RPC log interval")
	}
	p.mu.Lock()
	limit := p.logLimits[endpoint]
	p.mu.Unlock()
	if limit == 0 {
		raw, err := p.request(ctx, endpoint, method, params)
		var cap *fastLogLimit
		if !errors.As(err, &cap) {
			return raw, err
		}
		limit = cap.Max
		p.mu.Lock()
		p.logLimits[endpoint] = limit
		p.mu.Unlock()
	}
	merged := make([]json.RawMessage, 0)
	for start := from; start <= to; start += limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + limit - 1
		if end > to {
			end = to
		}
		part := map[string]any{}
		for key, value := range filter {
			part[key] = value
		}
		part["fromBlock"] = fmt.Sprintf("0x%x", start)
		part["toBlock"] = fmt.Sprintf("0x%x", end)
		raw, err := p.request(ctx, endpoint, method, []any{part})
		if err != nil {
			return nil, err
		}
		if err = validateFastResult(method, raw); err != nil {
			return nil, err
		}
		var events []json.RawMessage
		if err = json.Unmarshal(raw, &events); err != nil {
			return nil, err
		}
		merged = append(merged, events...)
	}
	return json.Marshal(merged)
}
