package browser

import (
	"log"
	"time"
)

func (p *Pool) KeepAlive(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			p.mu.Lock()
			for name, inst := range p.instances {
				if inst.browser == nil {
					continue
				}
				alive := true
				if _, err := inst.browser.Version(); err != nil {
					alive = false
				}
				if !alive {
					log.Printf("[%s] 浏览器进程异常退出, 等待重建", name)
					inst.close()
					delete(p.instances, name)
					continue
				}
			}
			p.mu.Unlock()
		}
	}()
}
