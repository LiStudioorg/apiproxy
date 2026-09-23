// Package account 提供每平台多账号（Profile）的轮换管理。
// 每个账号对应一个独立的浏览器 UserDataDir（/ proxy），互不干扰 Cookie 会话。
package account

import (
	"fmt"
	"sync"
	"time"

	"web2api/internal/config"
)

type Entry = config.AccountConfig

// Pool 管理单平台的一组账号，跟踪当前账号的今日请求量。
type Pool struct {
	mu      sync.Mutex
	name    string
	entries []Entry
	idx     int
	used    int64
	day     string
}

func New(name string, entries []Entry) *Pool {
	if len(entries) == 0 {
		entries = []Entry{{ProfileDir: "./profiles/" + name}}
	}
	return &Pool{name: name, entries: entries, day: today()}
}

func today() string { return time.Now().Format("2006-01-02") }

// Current 返回当前账号（不轮换）。
func (p *Pool) Current() Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDay()
	return p.entries[p.idx%len(p.entries)]
}

// MarkUsed 记一次请求。
func (p *Pool) MarkUsed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDay()
	p.used++
}

// Used 当前账号今日用量。
func (p *Pool) Used() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDay()
	return p.used
}

// ShouldRotate 判断当前账号是否已达到单日配额（limit=0 表示不限）。
func (p *Pool) ShouldRotate(limit int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDay()
	return limit > 0 && p.used >= limit && len(p.entries) > 1
}

// Rotate 切到下一个账号并清零今日用量，返回新账号。
func (p *Pool) Rotate() Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDay()
	p.idx++
	p.used = 0
	return p.entries[p.idx%len(p.entries)]
}

// Index 当前账号序号（日志用）。
func (p *Pool) Index() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idx%len(p.entries) + 1
}

func (p *Pool) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("%s[账号%d/%d @%s used=%d]", p.name, p.idx%len(p.entries)+1, len(p.entries), p.entries[p.idx%len(p.entries)].ProfileDir, p.used)
}

// rollDay 跨天时清空今日用量。
func (p *Pool) rollDay() {
	if d := today(); d != p.day {
		p.day = d
		p.used = 0
	}
}