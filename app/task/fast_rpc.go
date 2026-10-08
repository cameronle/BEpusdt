package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errFastNoResult = errors.New("RPC returned no result")

// Read-only, network-bound RPC pools. Errors never include endpoint paths/keys.
type fastRPC struct {
	chainID   int64
	endpoints []string
	client    *http.Client
	mu        sync.Mutex
	verified  map[string]bool
	cooldown  map[string]time.Time
	preferred map[string]int
	logLimits map[string]int64
}

func newFastRPC(chainID int64, endpoints []string, client *http.Client) (*fastRPC, error) {
	p := &fastRPC{chainID: chainID, client: client, verified: map[string]bool{}, cooldown: map[string]time.Time{}, preferred: map[string]int{}, logLimits: map[string]int64{}}
	if client == nil {
		p.client = &http.Client{Timeout: 5 * time.Second}
	}
	for _, raw := range endpoints {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return nil, errors.New("invalid RPC endpoint")
		}
		if raw != "" && !containsString(p.endpoints, raw) {
			p.endpoints = append(p.endpoints, raw)
		}
	}
	if chainID <= 0 || len(p.endpoints) == 0 {
		return nil, errors.New("missing network RPC endpoints")
	}
	return p, nil
}
func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func (p *fastRPC) request(ctx context.Context, endpoint, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid RPC request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "BEpusdt-Shop-Scanner/2")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("RPC transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RPC HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, errors.New("invalid RPC response size")
	}
	var envelope struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Version != "2.0" || string(envelope.ID) != "1" {
		return nil, errors.New("invalid RPC response envelope")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		if method == "eth_getLogs" {
			return nil, parseFastLogLimit(envelope.Error)
		}
		return nil, errors.New("RPC returned error")
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, errFastNoResult
	}
	return envelope.Result, nil
}
func (p *fastRPC) Call(ctx context.Context, method string, params any, out any) error {
	switch method {
	case "eth_blockNumber", "eth_getBlockByNumber", "eth_getLogs", "eth_getTransactionReceipt":
	default:
		return errors.New("non-read-only RPC method rejected")
	}
	p.mu.Lock()
	start := p.preferred[method]
	p.mu.Unlock()
	for n := 0; n < len(p.endpoints); n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		i := (start + n) % len(p.endpoints)
		endpoint := p.endpoints[i]
		key := endpoint + "|" + method
		p.mu.Lock()
		paused := time.Now().Before(p.cooldown[key])
		checked := p.verified[endpoint]
		p.mu.Unlock()
		if paused {
			continue
		}
		if !checked {
			raw, err := p.request(ctx, endpoint, "eth_chainId", []any{})
			var id string
			if err == nil {
				err = json.Unmarshal(raw, &id)
			}
			number, parseErr := parseFastHex(id)
			if err != nil || parseErr != nil || number != p.chainID {
				p.mu.Lock()
				p.cooldown[key] = time.Now().Add(15 * time.Second)
				p.mu.Unlock()
				continue
			}
			p.mu.Lock()
			p.verified[endpoint] = true
			p.mu.Unlock()
		}
		raw, err := p.requestAdaptive(ctx, endpoint, method, params)
		if err == nil {
			err = validateFastResult(method, raw)
		}
		if err == nil {
			err = json.Unmarshal(raw, out)
		}
		if err == nil {
			p.mu.Lock()
			p.preferred[method] = i
			p.mu.Unlock()
			return nil
		}
		if method == "eth_getTransactionReceipt" && errors.Is(err, errFastNoResult) {
			continue
		}
		p.mu.Lock()
		p.cooldown[key] = time.Now().Add(15 * time.Second)
		p.mu.Unlock()
	}
	return fmt.Errorf("all %d network RPC endpoints unavailable for %s", len(p.endpoints), method)
}
func parseFastHex(s string) (int64, error) {
	if !strings.HasPrefix(s, "0x") || len(s) <= 2 {
		return 0, errors.New("invalid RPC hex number")
	}
	n, err := strconv.ParseInt(s[2:], 16, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid RPC hex number")
	}
	return n, nil
}
