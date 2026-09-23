package proxy

import (
	"sync/atomic"
)

type Pool struct {
	list []string
	idx  uint64
}

func New(list []string) *Pool {
	clean := make([]string, 0, len(list))
	for _, p := range list {
		if p != "" {
			clean = append(clean, p)
		}
	}
	return &Pool{list: clean}
}

func (p *Pool) Next() string {
	if len(p.list) == 0 {
		return ""
	}
	i := atomic.AddUint64(&p.idx, 1)
	return p.list[(i-1)%uint64(len(p.list))]
}

func (p *Pool) Empty() bool {
	return len(p.list) == 0
}
