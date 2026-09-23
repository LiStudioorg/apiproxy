package rate

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
	sem      chan struct{}
	count    int64
	limit    int64
}

func New(maxConcurrent int, interval time.Duration, limit int64) *Limiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Limiter{
		interval: interval,
		sem:      make(chan struct{}, maxConcurrent),
		limit:    limit,
	}
}

func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l.sem }()

	l.mu.Lock()
	if l.limit > 0 && l.count >= l.limit {
		l.mu.Unlock()
		return fmt.Errorf("今日请求数已达上限 %d, 请使用备用账号后重试", l.limit)
	}
	wait := l.interval - time.Since(l.last)
	if wait > 0 {
		l.last = time.Now().Add(wait)
		l.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		l.last = time.Now()
		l.mu.Unlock()
	}
	return nil
}

func (l *Limiter) Done() {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
}

func (l *Limiter) Count() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

func (l *Limiter) Config() (int, time.Duration, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cap(l.sem), l.interval, l.limit
}
