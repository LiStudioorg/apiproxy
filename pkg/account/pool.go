package account

import (
	"fmt"
	"strings"
	"sync"
)

type Account struct {
	Name  string
	Proxy string
}

type Pool struct {
	mu       sync.Mutex
	accounts []Account
	idx      int
	count    int64
	limit    int64
}

func New(accounts []Account, limit int64) *Pool {
	return &Pool{accounts: accounts, limit: limit}
}

func (p *Pool) Pick() (Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.accounts) == 0 {
		return Account{}, fmt.Errorf("无可用账号")
	}
	if p.limit > 0 && p.count >= p.limit*int64(len(p.accounts)) {
		p.count = 0
	}
	a := p.accounts[p.idx%len(p.accounts)]
	p.idx++
	return a, nil
}

func (p *Pool) NoteSuccess() {
	p.mu.Lock()
	p.count++
	p.mu.Unlock()
}

func Describe(accounts []Account) string {
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
