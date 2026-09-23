package browser

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"

	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/stream"
)

var (
	ErrNotLoggedIn = errors.New("未登录")
	ErrDisabled    = errors.New("平台未启用")
	ErrSSEClosed   = errors.New("sse 流意外关闭")
)

type Pool struct {
	mu        sync.Mutex
	cfg       *config.Config
	instances map[string]*Instance
}

func NewPool(cfg *config.Config) *Pool {
	return &Pool{cfg: cfg, instances: map[string]*Instance{}}
}

type sink struct {
	keyword string
	ch      chan []byte
}

type Instance struct {
	name     string
	driver   platform.PlatformDriver
	profile  string
	proxy    string
	headless bool

	browser *rod.Browser
	page    *rod.Page

	watchStop func()
	reqURL    sync.Map
	sinkMu    sync.Mutex
	sink      *sink
}

func (p *Pool) Peek(d platform.PlatformDriver) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instances[d.Name()], nil
}

func (p *Pool) Ensure(d platform.PlatformDriver) (*Instance, error) {
	cfg, ok := p.cfg.GetPlatform(d.Name())
	if !ok || !cfg.Enabled {
		return nil, fmt.Errorf("%s: %w", d.Name(), ErrDisabled)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if inst, ok := p.instances[d.Name()]; ok {
		if inst.profile != cfg.ProfileDir || inst.proxy != cfg.Proxy {
			inst.close()
			delete(p.instances, d.Name())
		} else {
			return inst, nil
		}
	}

	inst, err := launch(d, cfg)
	if err != nil {
		return nil, err
	}
	p.instances[d.Name()] = inst
	return inst, nil
}

func launch(d platform.PlatformDriver, cfg config.PlatformConfig) (*Instance, error) {
	if err := ensureProfileDir(cfg.ProfileDir); err != nil {
		return nil, err
	}

	l := launcher.New().
		UserDataDir(cfg.ProfileDir).
		Headless(cfg.Headless).
		Set("--no-sandbox").
		Set("--mute-audio")
	if bin, ok := findChromium(); ok {
		l = l.Bin(bin)
	}
	if cfg.Proxy != "" {
		l = l.Proxy(cfg.Proxy)
	}

	binURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动浏览器: %w", err)
	}

	b := rod.New().ControlURL(binURL).MustConnect()
	page := stealth.MustPage(b)
	inst := &Instance{
		name:     d.Name(),
		driver:   d,
		profile:  cfg.ProfileDir,
		proxy:    cfg.Proxy,
		headless: cfg.Headless,
		browser:  b,
		page:     page,
	}

	restore := page.EnableDomain(&proto.NetworkEnable{})
	if restore != nil {
		defer restore()
	}
	inst.startWatch()

	if err := page.Navigate(d.LoginURL()); err != nil {
		inst.close()
		return nil, fmt.Errorf("打开 %s: %w", d.Name(), err)
	}
	page.MustWaitLoad()
	return inst, nil
}

func findChromium() (string, bool) {
	for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "chrome", "msedge"} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin, true
		}
	}
	return "", false
}

func ensureProfileDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 profile 目录: %w", err)
	}
	return nil
}

func (i *Instance) close() {
	if i.watchStop != nil {
		i.watchStop()
	}
	if i.page != nil {
		if le := i.page.Close(); le != nil {
			_ = le
		}
	}
	if i.browser != nil {
		if le := i.browser.Close(); le != nil {
			_ = le
		}
		i.browser = nil
		i.page = nil
	}
}

func (i *Instance) startWatch() {
	i.watchStop = i.page.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			i.reqURL.Store(e.RequestID, e.Request.URL)
		},
		func(e *proto.NetworkResponseReceived) {
			i.reqURL.Store(e.RequestID, e.Response.URL)
		},
		func(e *proto.NetworkDataReceived) {
			if len(e.Data) == 0 {
				return
			}
			v, ok := i.reqURL.Load(e.RequestID)
			if !ok {
				return
			}
			rawURL, _ := v.(string)
			i.sinkMu.Lock()
			s := i.sink
			i.sinkMu.Unlock()
			if s == nil {
				return
			}
			if !stream.MatchURL(rawURL, s.keyword) {
				return
			}
			select {
			case s.ch <- e.Data:
			default:
			}
		},
	)
}

func (i *Instance) setSink(keyword string, ch chan []byte) {
	i.sinkMu.Lock()
	i.sink = &sink{keyword: keyword, ch: ch}
	i.sinkMu.Unlock()
}

func (i *Instance) clearSink() {
	i.sinkMu.Lock()
	i.sink = nil
	i.sinkMu.Unlock()
}

func (i *Instance) Navigate(url string) error {
	return i.page.Navigate(url)
}

func (i *Instance) LoginStatus() (bool, error) {
	return i.driver.LoginCheck(i.page)
}

func (i *Instance) ensureLoggedIn() (bool, error) {
	if err := i.page.Navigate(i.driver.LoginURL()); err != nil {
		return false, err
	}
	i.page.MustWaitLoad()
	time.Sleep(800 * time.Millisecond)
	return i.driver.LoginCheck(i.page)
}

func (i *Instance) startNewChat() error {
	sel := i.driver.Selectors()
	if sel.NewChat == "" {
		return nil
	}
	btn, err := i.page.Timeout(5 * time.Second).Element(sel.NewChat)
	if err != nil {
		return nil
	}
	if btn.MustVisible() {
		btn.MustClick()
		time.Sleep(1200 * time.Millisecond)
	}
	return nil
}

func (i *Instance) sendMessage(text string) error {
	sel := i.driver.Selectors()
	inputEl, err := i.page.Timeout(15 * time.Second).Element(sel.Input)
	if err != nil {
		return fmt.Errorf("未找到输入框: %w", err)
	}
	inputEl.MustWaitVisible().MustClick()
	time.Sleep(300 * time.Millisecond)

	for _, r := range text {
		inputEl.MustType(input.Key(r))
		time.Sleep(time.Duration(20+rand.Intn(40)) * time.Millisecond)
	}

	time.Sleep(time.Duration(800+rand.Intn(1500)) * time.Millisecond)

	if btn, err := i.page.Timeout(3 * time.Second).Element(sel.Send); err == nil {
		if btn.MustVisible() {
			btn.MustClick()
			return nil
		}
	}
	if err := i.page.Keyboard.Type(input.Enter); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

func (i *Instance) Chat(ctx context.Context, text string, emit func(string)) error {
	loggedIn, err := i.ensureLoggedIn()
	if err != nil {
		return fmt.Errorf("检查登录状态: %w", err)
	}
	if !loggedIn {
		return fmt.Errorf("%s: %w", i.name, ErrNotLoggedIn)
	}
	if err := i.startNewChat(); err != nil {
		return fmt.Errorf("新建对话: %w", err)
	}

	dec := &stream.FrameDecoder{}
	ch := make(chan []byte, 8192)
	i.setSink(i.driver.StreamURL(), ch)
	defer i.clearSink()

	if err := i.sendMessage(text); err != nil {
		return err
	}

	const idleTimeout = 3 * time.Second
	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()

	var streamingDone bool
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw, ok := <-ch:
			if !ok {
				return ErrSSEClosed
			}
			dec.Feed(raw, func(p []byte) {
				delta, done := i.driver.ParseSSEChunk(p)
				if delta != "" {
					emit(delta)
					if !idle.Stop() {
						select {
						case <-idle.C:
						default:
						}
					}
					idle.Reset(idleTimeout)
				}
				if done {
					streamingDone = true
				}
			})
			if streamingDone {
				return nil
			}
		case <-idle.C:
			return nil
		}
	}
}

func (p *Pool) ApplyConfig() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, inst := range p.instances {
		cfg, ok := p.cfg.GetPlatform(name)
		if !ok || !cfg.Enabled {
			inst.close()
			delete(p.instances, name)
			continue
		}
		if inst.profile != cfg.ProfileDir || inst.proxy != cfg.Proxy {
			inst.close()
			delete(p.instances, name)
		}
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, inst := range p.instances {
		inst.close()
		delete(p.instances, name)
	}
}
