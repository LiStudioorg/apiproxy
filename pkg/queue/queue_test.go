package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueRunsJobs(t *testing.T) {
	q := New(16, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	var n atomic.Int64
	for i := 0; i < 10; i++ {
		if err := q.Submit(func(ctx context.Context) error {
			n.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() != 10 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n.Load() != 10 {
		t.Fatalf("only %d jobs ran", n.Load())
	}
}

func TestQueueFull(t *testing.T) {
	q := New(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	block := make(chan struct{})
	entered := make(chan struct{})
	_ = q.Submit(func(context.Context) error {
		close(entered)
		<-block
		return nil
	})
	<-entered // 保证 worker 占住
	if err := q.Submit(func(context.Context) error { return nil }); err != nil {
		t.Fatalf("submit 2 should succeed (1 pending allowed): %v", err)
	}
	if err := q.Submit(func(context.Context) error { return nil }); !errors.Is(err, ErrFull) {
		t.Fatalf("submit 3 should be full, got %v", err)
	}
	close(block)
}

func TestQueueShutdown(t *testing.T) {
	q := New(4, 2)
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	var n atomic.Int64
	for i := 0; i < 4; i++ {
		_ = q.Submit(func(context.Context) error { n.Add(1); time.Sleep(20 * time.Millisecond); return nil })
	}
	shutdownCtx, sc := context.WithTimeout(context.Background(), time.Second)
	defer sc()
	q.Shutdown(shutdownCtx)
	cancel()
	if n.Load() != 4 {
		t.Fatalf("worker stopped before draining, ran=%d", n.Load())
	}
}