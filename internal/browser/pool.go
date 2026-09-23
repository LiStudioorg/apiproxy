package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"web2api/pkg/account"
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
	accounts  map[string]*account.Pool
}

func NewPool(cfg *config.Config) *Pool {
	return &Pool{cfg: cfg, instances: map[string]*Instance{}, accounts: map[string]*account.Pool{}}
}

// SetAccountPools 注入每平台的账号池（在 main 装配阶段调用）。
func (p *Pool) SetAccountPools(pools map[string]*account.Pool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = pools
}

// accountPool 返回平台账号池；调用方必须持有 p.mu。
func (p *Pool) accountPool(name string) *account.Pool {
	if p == nil {
		return nil
	}
	return p.accounts[name]
}

// CurrentAccount 返回平台当前账号。
func (p *Pool) CurrentAccount(name string) account.Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool := p.accountPool(name)
	if pool != nil {
		return pool.Current()
	}
	return account.Entry{}
}

// ShouldRotate 判断当前账号是否需要按配额轮换。
func (p *Pool) ShouldRotate(name string, limit int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool := p.accountPool(name)
	if pool == nil {
		return false
	}
	return pool.ShouldRotate(limit)
}

// Rotate 关闭当前实例并切换账号，返回新账号。
func (p *Pool) Rotate(d platform.PlatformDriver) (account.Entry, error) {
	p.mu.Lock()
	if inst, ok := p.instances[d.Name()]; ok {
		inst.close()
		delete(p.instances, d.Name())
	}
	pool := p.accountPool(d.Name())
	p.mu.Unlock()
	if pool == nil {
		return account.Entry{}, errors.New("无账号池")
	}
	entry := pool.Rotate()
	log.Printf("[account] %s 切换账号 -> %s", d.Name(), entry.ProfileDir)
	return entry, nil
}

// Instance = 一个平台一个浏览器内核（Cookies/会话共用），
// 但每条流式对话独占一个 Tab（stealth 新页面），对话结束后关闭 Tab 释放渲染进程。
// 这样：① 可并行多条流式（max_concurrency 决定并发数）；
// ② 串行聊天不会累积内存（Tab 用完即焚）。base 进程固定为浏览器内核本体。
type Instance struct {
	name     string
	driver   platform.PlatformDriver
	profile  string
	proxy    string
	headless bool

	browser *rod.Browser
	page    *rod.Page // 控制页：登录画面 / viewer / LoginStatus，不参与聊天
}

func (p *Pool) Peek(d platform.PlatformDriver) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instances[d.Name()], nil
}

// Page 返回实例页面（内嵌浏览器画面 viewer 用）。
func (inst *Instance) Page() *rod.Page {
	if inst == nil {
		return nil
	}
	return inst.page
}

// OpenLogin 确保浏览器实例存在并导航到登录页（本机点击"打开登录页"时调用）。
func (p *Pool) Ensure(d platform.PlatformDriver) (*Instance, error) {
	cfg, ok := p.cfg.GetPlatform(d.Name())
	if !ok || !cfg.Enabled {
		return nil, fmt.Errorf("%s: %w", d.Name(), ErrDisabled)
	}

	entry := p.CurrentAccount(d.Name())
	if entry.ProfileDir == "" {
		entry = account.Entry{ProfileDir: cfg.ProfileDir, Proxy: cfg.Proxy}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	wantHeadless := effectiveHeadless(cfg.Headless)
	if inst, ok := p.instances[d.Name()]; ok {
		if inst.profile != entry.ProfileDir || inst.proxy != entry.Proxy || inst.headless != wantHeadless {
			inst.close()
			delete(p.instances, d.Name())
		} else {
			return inst, nil
		}
	}

	inst, err := launch(d, entry, wantHeadless)
	if err != nil {
		return nil, err
	}
	p.instances[d.Name()] = inst
	return inst, nil
}

// effectiveHeadless 无桌面环境时强制 headless，保证在服务器/手机端也能跑。
func effectiveHeadless(prefer bool) bool {
	if prefer {
		return true
	}
	return !hasDisplay()
}

// hasDisplay 判断当前是否有可用图形桌面。
func hasDisplay() bool {
	if runtime.GOOS == "linux" {
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return true // windows/darwin 始终有图形会话
}

func (p *Pool) OpenLogin(d platform.PlatformDriver) (*Instance, error) {
	inst, err := p.Ensure(d)
	if err != nil {
		return nil, err
	}
	if err := inst.page.Navigate(d.LoginURL()); err != nil {
		return nil, fmt.Errorf("导航到登录页: %w", err)
	}
	inst.page.MustWaitLoad()
	return inst, nil
}

func launch(d platform.PlatformDriver, entry account.Entry, headless bool) (*Instance, error) {
	if err := ensureProfileDir(entry.ProfileDir); err != nil {
		return nil, err
	}
	cleanStaleLocks(entry.ProfileDir)

	l := launcher.New().
		UserDataDir(entry.ProfileDir).
		Headless(headless).
		Set("--no-sandbox").
		Set("--mute-audio").
		// ---- 省内存 / 减进程 ----
		Set("--no-zygote").   // 去掉 zygote 子进程
		Set("--disable-gpu"). // 无 GPU 环境用软渲染也够（canvas 指纹由 stealth 兜底）
		Set("--disable-extensions").
		Set("--disable-sync").
		Set("--disable-translate").
		Set("--disable-notifications")
	if bin, ok := findChromium(); ok {
		l = l.Bin(bin)
	}
	if entry.Proxy != "" {
		l = l.Proxy(entry.Proxy)
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
		profile:  entry.ProfileDir,
		proxy:    entry.Proxy,
		headless: headless,
		browser:  b,
		page:     page,
	}

	if err := page.Navigate(d.LoginURL()); err != nil {
		inst.close()
		return nil, fmt.Errorf("打开 %s: %w", d.Name(), err)
	}
	page.MustWaitLoad()
	return inst, nil
}

// checkDesktopSupport 已移除：不额外探测桌面环境，让 launcher 自行报错。

func findChromium() (string, bool) {
	for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "chrome", "msedge"} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin, true
		}
	}
	return "", false
}

func cleanStaleLocks(dir string) {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			log.Printf("[launcher] 清理残留锁文件: %s", name)
		}
	}
}

func ensureProfileDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 profile 目录: %w", err)
	}
	return nil
}

func (i *Instance) close() {
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

// newChatTab 每次对话开一个 stealth 新 Tab，共享同一浏览器内核（Cookie 会话）。
func (i *Instance) newChatTab() *rod.Page {
	return stealth.MustPage(i.browser)
}

func (i *Instance) Navigate(url string) error {
	return i.page.Navigate(url)
}

func (i *Instance) LoginStatus() (bool, error) {
	return i.driver.LoginCheck(i.page)
}

func (i *Instance) Chat(ctx context.Context, text string, emit func(string)) error {
	p := i.newChatTab()
	if p == nil {
		return errors.New("创建聊天 Tab 失败")
	}
	defer func() { _ = p.Close() }()

	return runChat(ctx, i.driver, p, text, emit)
}

// runChat 在独立 Tab 上执行一轮对话：导航 → 登录态检查 → 新对话 → 发消息 → 抓 SSE 流式回包。
func runChat(ctx context.Context, d platform.PlatformDriver, p *rod.Page, text string, emit func(string)) error {
	// 聊天 Tab 需打开 Network 域才能收到 dataReceived
	if restore := p.EnableDomain(&proto.NetworkEnable{}); restore != nil {
		defer restore()
	}
	if err := p.Navigate(d.LoginURL()); err != nil {
		return fmt.Errorf("检查登录状态: %w", err)
	}
	p.MustWaitLoad()
	time.Sleep(800 * time.Millisecond)
	loggedIn, err := d.LoginCheck(p)
	if err != nil {
		return fmt.Errorf("检查登录状态: %w", err)
	}
	if !loggedIn {
		return fmt.Errorf("%s: %w", d.Name(), ErrNotLoggedIn)
	}

	if sel := d.Selectors(); sel.NewChat != "" {
		if btn, e := p.Timeout(5 * time.Second).Element(sel.NewChat); e == nil && btn.MustVisible() {
			btn.MustClick()
			time.Sleep(1200 * time.Millisecond)
		}
	}

	dec := &stream.FrameDecoder{}
	ch := make(chan []byte, 8192)
	stop := watchChat(p, d.StreamURL(), ch)
	defer stop()

	if err := sendMessage(p, d.Selectors(), text); err != nil {
		return err
	}

	const idleTimeout = 3 * time.Second
	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()

	var streamingDone bool
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw, ok := <-ch:
			if !ok {
				return ErrSSEClosed
			}
			dec.Feed(raw, func(pkt []byte) {
				delta, done, perr := d.ParseSSEChunk(pkt)
				if perr != nil {
					lastErr = perr
					streamingDone = true
					return
				}
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
				if lastErr != nil {
					return lastErr
				}
				return nil
			}
		case <-idle.C:
			return nil
		}
	}
}

// watchChat 挂到单个聊天 Tab 上抓取 SSE 数据（与对话一一对应，互不干扰）。
func watchChat(p *rod.Page, keyword string, ch chan []byte) (stop func()) {
	var reqURL sync.Map
	return p.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			reqURL.Store(e.RequestID, e.Request.URL)
		},
		func(e *proto.NetworkResponseReceived) {
			reqURL.Store(e.RequestID, e.Response.URL)
		},
		func(e *proto.NetworkDataReceived) {
			if len(e.Data) == 0 {
				return
			}
			v, ok := reqURL.Load(e.RequestID)
			if !ok {
				return
			}
			rawURL, _ := v.(string)
			if !stream.MatchURL(rawURL, keyword) {
				return
			}
			select {
			case ch <- e.Data:
			default:
			}
		},
	)
}

// sendMessage 在指定页面上输入并发送。
func sendMessage(p *rod.Page, sel platform.Selectors, text string) error {
	inputEl, err := p.Timeout(15 * time.Second).Element(sel.Input)
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

	if btn, err := p.Timeout(3 * time.Second).Element(sel.Send); err == nil {
		if btn.MustVisible() {
			btn.MustClick()
			return nil
		}
	}
	if err := p.Keyboard.Type(input.Enter); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return nil
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
		entry := account.Entry{}
		if pool := p.accountPool(name); pool != nil {
			entry = pool.Current()
		}
		if entry.ProfileDir == "" {
			entry = account.Entry{ProfileDir: cfg.ProfileDir, Proxy: cfg.Proxy}
		}
		if inst.profile != entry.ProfileDir || inst.proxy != entry.Proxy || inst.headless != effectiveHeadless(cfg.Headless) {
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
