// Package queue 提供有界请求队列 + 固定 worker 池。
// 超载时 Submit 立即返回 ErrFull，由调用方返回 HTTP 429。
package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var ErrFull = errors.New("请求队列已满，请稍后重试")

type Job func(ctx context.Context) error

type Queue struct {
	ch      chan Job
	workers int
	wg      sync.WaitGroup
	started atomic.Bool
	dropped atomic.Int64
}

// New 创建容量 capacity、worker 数 workers 的队列。
func New(capacity, workers int) *Queue {
	if capacity <= 0 {
		capacity = 128
	}
	if workers <= 0 {
		workers = 4
	}
	return &Queue{ch: make(chan Job, capacity), workers: workers}
}

func (q *Queue) Start(ctx context.Context) {
	if !q.started.CompareAndSwap(false, true) {
		return
	}
	q.wg.Add(q.workers)
	for i := 0; i < q.workers; i++ {
		go func() {
			defer q.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-q.ch:
					if !ok {
						return
					}
					if job != nil {
						_ = job(ctx)
					}
				}
			}
		}()
	}
}

// Submit 非阻塞投递；队列满返回 ErrFull。
func (q *Queue) Submit(job Job) error {
	select {
	case q.ch <- job:
		return nil
	default:
		q.dropped.Add(1)
		return ErrFull
	}
}

func (q *Queue) Dropped() int64 { return q.dropped.Load() }

// Shutdown 关闭入口并等待所有 worker 跑完当前任务。
func (q *Queue) Shutdown(ctx context.Context) {
	close(q.ch)
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}