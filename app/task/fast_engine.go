package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type fastRange struct {
	From     int64   `json:"from"`
	To       int64   `json:"to"`
	OrderIDs []int64 `json:"order_ids,omitempty"`
}
type fastState struct {
	Version             int              `json:"version"`
	Network             string           `json:"network"`
	Forward             int64            `json:"forward"`
	Target              int64            `json:"target"`
	Pending             []fastRange      `json:"pending"`
	DoneOrders          map[string]int64 `json:"done_orders"`
	LastRealtimeSuccess time.Time        `json:"last_realtime_success"`
	LastBackfillSuccess time.Time        `json:"last_backfill_success"`
	RealtimeError       string           `json:"realtime_error,omitempty"`
	BackfillError       string           `json:"backfill_error,omitempty"`
	Matches             int              `json:"matches"`
	UpdatedAt           time.Time        `json:"updated_at"`
}
type fastEngine struct {
	mu                       sync.Mutex
	liveMu                   sync.Mutex
	backMu                   sync.Mutex
	path                     string
	window, maxLive, maxBack int64
	state                    fastState
	scan                     func(context.Context, evmBlock) (int, error)
	backScan                 func(context.Context, evmBlock) (int, error)
}

func newFastEngine(network, path string, window, maxLive, maxBack int64, scan func(context.Context, evmBlock) (int, error)) (*fastEngine, error) {
	if network != "bsc" && network != "polygon" || window <= 0 || maxLive < window || maxBack <= 0 || maxBack > 1000 || scan == nil {
		return nil, errors.New("invalid scanner configuration")
	}
	e := &fastEngine{path: path, window: window, maxLive: maxLive, maxBack: maxBack, scan: scan, backScan: scan, state: fastState{Version: 1, Network: network, Pending: []fastRange{}, DoneOrders: map[string]int64{}}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &e.state); err != nil {
		return nil, err
	}
	if e.state.Version != 1 || e.state.Network != network || e.state.Forward < 0 || e.state.Target < 0 || e.state.DoneOrders == nil {
		return nil, errors.New("invalid persisted scanner state")
	}
	for _, r := range e.state.Pending {
		if r.From <= 0 || r.To < r.From {
			return nil, errors.New("invalid persisted recovery interval")
		}
	}
	return e, nil
}
func (e *fastEngine) saveLocked() error {
	e.state.UpdatedAt = time.Now().UTC()
	for id, at := range e.state.DoneOrders {
		if at < time.Now().Add(-7*24*time.Hour).Unix() {
			delete(e.state.DoneOrders, id)
		}
	}
	b, err := json.MarshalIndent(e.state, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(e.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(e.path), ".scanner-state-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, e.path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(e.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (e *fastEngine) Snapshot() fastState {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.state)
	var s fastState
	_ = json.Unmarshal(b, &s)
	return s
}
func (e *fastEngine) AddBackfill(b evmBlock, ids []int64) error {
	if b.From <= 0 || b.To < b.From {
		return errors.New("invalid recovery interval")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Pending = mergeFastRanges(append(e.state.Pending, fastRange{From: b.From, To: b.To, OrderIDs: append([]int64(nil), ids...)}))
	return e.saveLocked()
}
func (e *fastEngine) Realtime(ctx context.Context, target int64) error {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if target <= 0 {
		return errors.New("invalid confirmed target")
	}
	e.mu.Lock()
	e.state.Target = target
	if e.state.Forward == 0 {
		e.state.Forward = target - e.window
		if e.state.Forward < 0 {
			e.state.Forward = 0
		}
		if err := e.saveLocked(); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	if target-e.state.Forward > e.maxLive {
		gap := fastRange{From: e.state.Forward + 1, To: target - e.window}
		e.state.Pending = mergeFastRanges(append(e.state.Pending, gap))
		e.state.Forward = target - e.window
		if err := e.saveLocked(); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	from := e.state.Forward + 1
	to := target
	if to > from+e.maxLive-1 {
		to = from + e.maxLive - 1
	}
	e.mu.Unlock()
	if from > to {
		return nil
	}
	count, err := e.scan(ctx, evmBlock{From: from, To: to})
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.state.RealtimeError = err.Error()
		_ = e.saveLocked()
		return err
	}
	e.state.Forward = to
	e.state.LastRealtimeSuccess = time.Now().UTC()
	e.state.RealtimeError = ""
	e.state.Matches += count
	return e.saveLocked()
}
func (e *fastEngine) Backfill(ctx context.Context) error {
	e.backMu.Lock()
	defer e.backMu.Unlock()
	e.mu.Lock()
	if len(e.state.Pending) == 0 {
		e.mu.Unlock()
		return nil
	}
	r := e.state.Pending[0]
	b := evmBlock{From: r.From, To: r.To}
	if b.To > b.From+e.maxBack-1 {
		b.To = b.From + e.maxBack - 1
	}
	e.mu.Unlock()
	count, err := e.backScan(ctx, b)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.state.BackfillError = err.Error()
		_ = e.saveLocked()
		return err
	}
	remaining := make([]fastRange, 0)
	touched := map[int64]bool{}
	for _, r := range e.state.Pending {
		if r.To < b.From || r.From > b.To {
			remaining = append(remaining, r)
			continue
		}
		for _, id := range r.OrderIDs {
			touched[id] = true
		}
		if r.From < b.From {
			left := r
			left.To = b.From - 1
			remaining = append(remaining, left)
		}
		if r.To > b.To {
			right := r
			right.From = b.To + 1
			remaining = append(remaining, right)
		}
	}
	e.state.Pending = mergeFastRanges(remaining)
	for _, r := range remaining {
		for _, id := range r.OrderIDs {
			delete(touched, id)
		}
	}
	for id := range touched {
		e.state.DoneOrders[strconv.FormatInt(id, 10)] = time.Now().Unix()
	}
	e.state.LastBackfillSuccess = time.Now().UTC()
	e.state.BackfillError = ""
	e.state.Matches += count
	return e.saveLocked()
}

func mergeFastRanges(rs []fastRange) []fastRange {
	sort.Slice(rs, func(i, j int) bool { return rs[i].From < rs[j].From })
	out := make([]fastRange, 0, len(rs))
	for _, r := range rs {
		if len(out) == 0 || r.From > out[len(out)-1].To+1 {
			out = append(out, r)
			continue
		}
		last := &out[len(out)-1]
		if r.To > last.To {
			last.To = r.To
		}
		seen := map[int64]bool{}
		for _, id := range last.OrderIDs {
			seen[id] = true
		}
		for _, id := range r.OrderIDs {
			if !seen[id] {
				last.OrderIDs = append(last.OrderIDs, id)
				seen[id] = true
			}
		}
		sort.Slice(last.OrderIDs, func(i, j int) bool { return last.OrderIDs[i] < last.OrderIDs[j] })
	}
	return out
}
