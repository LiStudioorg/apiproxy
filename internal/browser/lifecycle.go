package browser

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// maxBrowserRSS 单浏览器 RSS 上限（MB）。超过且没有进行中的对话时自动重启，
// 避免长期运行后内存无限上涨。默认 1.5G。
const maxBrowserRSS = 1536

// activeChats 进行中的聊天 Tab 数（0 时才允许内存超限重启）。
var activeChats int32

func incActiveChat()         { atomic.AddInt32(&activeChats, 1) }
func decActiveChat()         { atomic.AddInt32(&activeChats, -1) }
func activeChatCount() int32 { return atomic.LoadInt32(&activeChats) }

// KeepAlive 周期性检查浏览器健康：进程挂了自动重建；RSS 超阈值且空闲时重启回收内存。
func (p *Pool) KeepAlive(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			p.mu.Lock()
			b := p.browser
			profile := p.profile
			p.mu.Unlock()
			if b == nil {
				continue
			}
			if _, err := b.Version(); err != nil {
				log.Printf("[pool] 浏览器进程异常退出, 自动重建")
				p.resetBrowser()
				go p.Prestart()
				continue
			}
			// 内存阈值守护：仅在没有任何进行中对话时重启，避免打断流式输出
			if rss := browserRSS(profile); rss > maxBrowserRSS && activeChatCount() == 0 {
				log.Printf("[pool] 浏览器内存 %dMB 超过 %dMB 且空闲，重启回收", rss, maxBrowserRSS)
				p.resetBrowser()
				go p.Prestart()
			}
		}
	}()
}

// resetBrowser 关闭浏览器并清空会话句柄（画面/聊天 Tab 随进程退出销毁）。
func (p *Pool) resetBrowser() {
	p.mu.Lock()
	b := p.browser
	p.browser = nil
	for name, inst := range p.instances {
		inst.page = nil
		delete(p.instances, name)
	}
	p.mu.Unlock()
	if b != nil {
		go func() { _ = b.Close() }()
	}
}

// browserRSS 统计持有 profile 目录的进程总 RSS（KB→MB）；非 Linux 返回 0。
func browserRSS(profile string) uint64 {
	if runtime.GOOS != "linux" || profile == "" {
		return 0
	}
	abs, err := filepath.Abs(profile)
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var totalKB uint64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || !strings.Contains(string(cmd), abs) {
			continue
		}
		st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(st), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						totalKB += kb
					}
				}
				break
			}
		}
		_ = pid
	}
	return totalKB / 1024
}
