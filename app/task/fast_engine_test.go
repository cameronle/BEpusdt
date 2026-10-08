package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFastEngineReconnectKeepsGapAndPrioritizesTip(t *testing.T) {
	var scanned []evmBlock
	e, err := newFastEngine("bsc", filepath.Join(t.TempDir(), "bsc.json"), 40, 200, 25, func(ctx context.Context, b evmBlock) (int, error) { scanned = append(scanned, b); return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Realtime(context.Background(), 200); err != nil {
		t.Fatal(err)
	}
	if err = e.AddBackfill(evmBlock{From: 50, To: 200}, []int64{10}); err != nil {
		t.Fatal(err)
	}
	if err = e.Realtime(context.Background(), 20000); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot()
	last := scanned[len(scanned)-1]
	if last.From != 19961 || last.To != 20000 || s.Forward != 20000 || len(s.Pending) != 1 || s.Pending[0].From != 50 || s.Pending[0].To != 19960 {
		t.Fatalf("reconnect did not retain/coalesce old coverage while prioritizing tip: last=%+v state=%+v", last, s)
	}
}

func TestFastEngineRealtimeNotBlockedByFailedBackfillAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bsc.json")
	started := make(chan struct{})
	release := make(chan struct{})
	fail := true
	scan := func(ctx context.Context, b evmBlock) (int, error) {
		if b.From == 1 {
			close(started)
			<-release
			if fail {
				return 0, errors.New("isolated fixture outage")
			}
		}
		return 0, nil
	}
	e, err := newFastEngine("bsc", path, 40, 200, 25, scan)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AddBackfill(evmBlock{From: 1, To: 100}, []int64{10}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- e.Backfill(context.Background()) }()
	<-started
	live := make(chan error, 1)
	go func() { live <- e.Realtime(context.Background(), 200) }()
	select {
	case err := <-live:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("realtime blocked behind historical RPC")
	}
	close(release)
	if err = <-completed; err == nil {
		t.Fatal("failed backfill acknowledged")
	}
	restored, err := newFastEngine("bsc", path, 40, 200, 25, func(context.Context, evmBlock) (int, error) { return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	s := restored.Snapshot()
	if s.Forward != 200 || len(s.Pending) != 1 || s.Pending[0].From != 1 || s.DoneOrders["10"] != 0 {
		t.Fatalf("lost failure or realtime progress: %+v", s)
	}
	if err = restored.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = restored.Snapshot()
	if s.Pending[0].From != 26 || s.DoneOrders["10"] != 0 {
		t.Fatalf("partial range marked complete: %+v", s)
	}
	for i := 0; i < 3; i++ {
		if err = restored.Backfill(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s = restored.Snapshot()
	if len(s.Pending) != 0 || s.DoneOrders["10"] == 0 {
		t.Fatalf("whole range completion not persisted: %+v", s)
	}
}
