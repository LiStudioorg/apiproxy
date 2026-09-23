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

// Pool = 全平台共用的“唯一一个无头浏览器进程”。
// 各平台是它内部的 Tab，Cookie 按域名天然隔离；内存只占一份。
//   - 聊天 Tab：每条流式对话独立开、用完即关（释放渲染进程）；
//   - 登录画面 Tab：仅在「浏览器画面」打开期间存在，关闭即销毁；
//   - 除此之外浏览器不持有任何空闲 Tab，内存保持最低。
type Pool struct {
	mu        sync.Mutex
	launchMu  sync.Mutex // 串行化浏览器启动，避免并发重复拉起
	cfg       *config.Config
	browser   *rod.Browser
	profile   string // 实际启动时的 profile 目录（检测配置变化）
	proxy     string // 实际启动时的代理（检测配置变化）
	accounts  map[string]*account.Pool
	instances map[string]*Instance
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

// CurrentAccount 返回平台当前账号（仅作统计/轮换索引）。
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

// Rotate 单浏览器模式：只推进统计用的账号索引，不重启浏览器（各平台 Cookie 共用一个 profile）。
func (p *Pool) Rotate(d platform.PlatformDriver) (account.Entry, error) {
	p.mu.Lock()
	pool := p.accountPool(d.Name())
	p.mu.Unlock()
	if pool == nil {
		return account.Entry{}, errors.New("无账号池")
	}
	entry := pool.Rotate()
	log.Printf("[account] %s 切换统计账号 -> %s（单浏览器模式，Cookie 共用；如需真正切号请用浏览器画面重新登录）", d.Name(), entry.ProfileDir)
	return entry, nil
}

// Instance = 一个平台的会话句柄（不再对应独立浏览器）。
// page 是该平台的“登录画面”Tab，仅浏览器画面打开期间存在，可为 nil。
type Instance struct {
	name         string
	driver       platform.PlatformDriver
	pool         *Pool
	page         *rod.Page
	logged       *bool                // 已知登录态缓存（无画面 Tab 时避免反复开临时 Tab）
	extraHeaders proto.NetworkHeaders // 通过「导入登录态」带入的外部请求头（browser 自带头除外）
}

// Peek 返回平台会话句柄；浏览器未启动或平台未注册时返回 nil。
func (p *Pool) Peek(d platform.PlatformDriver) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instances[d.Name()], nil
}

// Page 返回登录画面 Tab（浏览器画面 viewer 用），未打开时为 nil。
func (inst *Instance) Page() *rod.Page {
	if inst == nil {
		return nil
	}
	return inst.page
}

// Ensure 注册/返回平台会话句柄，按需拉起唯一浏览器进程。
func (p *Pool) Ensure(d platform.PlatformDriver) (*Instance, error) {
	pc, ok := p.cfg.GetPlatform(d.Name())
	if !ok || !pc.Enabled {
		return nil, fmt.Errorf("%s: %w", d.Name(), ErrDisabled)
	}
	if _, err := p.ensureBrowser(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inst := p.instances[d.Name()]
	if inst == nil {
		inst = &Instance{name: d.Name(), driver: d, pool: p}
		p.instances[d.Name()] = inst
	}
	return inst, nil
}

// Running 唯一浏览器是否已常驻启动。
func (p *Pool) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.browser != nil
}

// OpenViewer 供「浏览器画面」使用：确保浏览器与该平台画面 Tab 就绪，
// 登录页导航在后台重试（ERR_ABORTED 等临时错误不会打断画面连接）。
func (p *Pool) OpenViewer(d platform.PlatformDriver) (*Instance, error) {
	return p.openView(d)
}

// openView 打开（或复用）该平台的登录画面 Tab。
func (p *Pool) openView(d platform.PlatformDriver) (*Instance, error) {
	inst, err := p.Ensure(d)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if inst.page != nil {
		p.mu.Unlock()
		return inst, nil
	}
	p.mu.Unlock()

	b, err := p.ensureBrowser()
	if err != nil {
		return nil, err
	}
	pg := stealth.MustPage(b)
	p.mu.Lock()
	inst.page = pg
	p.mu.Unlock()

	go func() {
		for i := 0; i < 5; i++ {
			if err := pg.Navigate(d.LoginURL()); err == nil {
				return
			}
			time.Sleep(time.Duration(600+i*300) * time.Millisecond)
		}
	}()
	return inst, nil
}

// CloseView 关闭该平台的登录画面 Tab（浏览器画面关闭时调用），并缓存最终登录态。
func (p *Pool) CloseView(name string) {
	p.mu.Lock()
	inst := p.instances[name]
	if inst == nil || inst.page == nil {
		p.mu.Unlock()
		return
	}
	pg := inst.page
	inst.page = nil
	p.mu.Unlock()

	if ok, err := inst.driver.LoginCheck(pg); err == nil {
		p.mu.Lock()
		inst.logged = &ok
		p.mu.Unlock()
	}
	_ = pg.Close()
}

// CachedLogged 返回缓存的登录态（可能为 nil=未知）。只会返回上一次真实检测/关闭画面的结果，
// 不访问浏览器，供管理界面高频轮询状态使用。
func (inst *Instance) CachedLogged() *bool {
	if inst == nil {
		return nil
	}
	inst.pool.mu.Lock()
	defer inst.pool.mu.Unlock()
	if inst.logged == nil {
		return nil
	}
	v := *inst.logged
	return &v
}

// LoginStatus 检查登录态：优先用已打开的画面 Tab 实时检测；
// 没有画面 Tab 时开一个临时 Tab 检测一次并缓存（用完即关，省内存）。
// 导航/加载带超时，网络异常时不会无限阻塞调用方。
func (inst *Instance) LoginStatus() (bool, error) {
	if inst == nil {
		return false, errors.New("实例不存在")
	}
	p := inst.pool
	p.mu.Lock()
	pg := inst.page
	b := p.browser
	p.mu.Unlock()

	if pg != nil {
		tp := pg.Timeout(15 * time.Second)
		ok, err := inst.driver.LoginCheck(tp)
		if err == nil {
			p.mu.Lock()
			inst.logged = &ok
			p.mu.Unlock()
		}
		return ok, err
	}
	if b == nil {
		return false, errors.New("浏览器未启动")
	}
	tmp := stealth.MustPage(b)
	defer func() { _ = tmp.Close() }()
	if stopHard, err := blockHeavyResources(tmp); err == nil {
		defer stopHard()
	}
	tp := tmp.Timeout(20 * time.Second)
	if err := tp.Navigate(inst.driver.LoginURL()); err != nil {
		return false, err
	}
	tp.MustWaitLoad()
	time.Sleep(600 * time.Millisecond)
	ok, err := inst.driver.LoginCheck(tp)
	if err == nil {
		p.mu.Lock()
		inst.logged = &ok
		p.mu.Unlock()
	}
	return ok, err
}

// Navigate 将已打开的画面 Tab 导航到指定地址（画面 Tab 未开时返回错误）。
func (inst *Instance) Navigate(url string) error {
	if inst == nil || inst.page == nil {
		return errors.New("登录画面尚未打开")
	}
	return inst.page.Navigate(url)
}

// Chat 每次对话开一个独立 stealth Tab，共享唯一浏览器的 Cookie，对话结束即关（释放渲染进程）。
func (inst *Instance) Chat(ctx context.Context, text string, emit func(string)) error {
	b, err := inst.pool.ensureBrowser()
	if err != nil {
		return err
	}
	extra := inst.extraHeadersSnapshot()
	pg := stealth.MustPage(b)
	defer func() { _ = pg.Close() }()
	incActiveChat()
	defer decActiveChat()
	return runChat(ctx, inst.driver, pg, text, emit, extra)
}

// ensureBrowser 懒加载拉起唯一浏览器进程（省内存参数已调优），并发调用只启动一次。
func (p *Pool) ensureBrowser() (*rod.Browser, error) {
	p.mu.Lock()
	if p.browser != nil {
		b := p.browser
		p.mu.Unlock()
		return b, nil
	}
	p.mu.Unlock()

	p.launchMu.Lock()
	defer p.launchMu.Unlock()

	p.mu.Lock()
	if p.browser != nil {
		b := p.browser
		p.mu.Unlock()
		return b, nil
	}
	srv := p.cfg.GetServer()
	profile := srv.ProfileDir
	if profile == "" {
		profile = "./profiles"
	}
	p.mu.Unlock()

	if err := ensureProfileDir(profile); err != nil {
		return nil, err
	}
	cleanStaleLocks(profile)

	l := launcher.New().
		UserDataDir(profile).
		Headless(true).
		Set("--headless", "new"). // new 模式在 Termux 无 X 环境也能正常无头运行
		Set("--no-sandbox").
		Set("--mute-audio").
		// ---- 省内存 / 减进程（单浏览器，重点压低内存） ----
		// 必须 --no-zygote：Termux 版 chromium 不开它反而会起 5 个 zygote（每个最大 ~190MB）
		Set("--no-zygote").
		Set("--disable-gpu").
		Set("--disable-extensions").
		Set("--disable-sync").
		Set("--disable-translate").
		Set("--disable-notifications").
		Set("--disable-dev-shm-usage").
		Set("--disable-background-networking").
		Set("--disable-component-update").
		Set("--no-first-run").
		Set("--renderer-process-limit", "1"). // 渲染进程上限=1：关 Tab 后 Chrome 最多保留 1 个渲染进程，内存最低
		Set("--window-size", "800,600").
		Set("--disable-features", "Translate,MediaRouter,OptimizationHints")
	if bin, ok := findChromium(); ok {
		l = l.Bin(bin)
	}
	if srv.Proxy != "" {
		l = l.Proxy(srv.Proxy)
	}

	binURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动浏览器: %w", err)
	}
	b := rod.New().ControlURL(binURL).MustConnect()

	p.mu.Lock()
	p.browser = b
	p.profile = profile
	p.proxy = srv.Proxy
	p.mu.Unlock()
	log.Printf("[pool] 单浏览器已启动（无头常驻, profile=%s）", profile)
	return b, nil
}

// Prestart 启动时拉起唯一浏览器（有启用平台才拉），并为每个启用平台注册会话句柄。
// 不打开任何 Tab —— 内存只有浏览器本体，画面/聊天 Tab 按需创建、用完即关。
func (p *Pool) Prestart() {
	anyEnabled := false
	for _, d := range platform.All() {
		if pc, ok := p.cfg.GetPlatform(d.Name()); ok && pc.Enabled {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		return
	}
	if _, err := p.ensureBrowser(); err != nil {
		log.Printf("[pool] 浏览器预启动失败: %v", err)
		return
	}
	n := 0
	for _, d := range platform.All() {
		pc, ok := p.cfg.GetPlatform(d.Name())
		if !ok || !pc.Enabled {
			continue
		}
		p.mu.Lock()
		if p.instances[d.Name()] == nil {
			p.instances[d.Name()] = &Instance{name: d.Name(), driver: d, pool: p}
		}
		p.mu.Unlock()
		n++
	}
	log.Printf("[pool] 浏览器常驻就绪，已注册启用平台 %d 个（按需开 Tab，用完即关）", n)
}

// ApplyConfig 配置热更新：移除禁用平台句柄；profile/代理变化时重启浏览器（下次使用时按新配置拉起）。
func (p *Pool) ApplyConfig() {
	srv := p.cfg.GetServer()
	wantProfile := srv.ProfileDir
	if wantProfile == "" {
		wantProfile = "./profiles"
	}

	p.mu.Lock()
	needRestart := p.browser != nil && (p.proxy != srv.Proxy || p.profile != wantProfile)

	for name, inst := range p.instances {
		pc, ok := p.cfg.GetPlatform(name)
		if !ok || !pc.Enabled {
			pg := inst.page
			inst.page = nil
			if pg != nil {
				go func(pg *rod.Page) { _ = pg.Close() }(pg)
			}
			delete(p.instances, name)
		}
	}

	var old *rod.Browser
	if needRestart {
		old = p.browser
		p.browser = nil
		p.proxy = srv.Proxy
		p.profile = wantProfile
		for name, inst := range p.instances {
			if inst.page != nil {
				pg := inst.page
				inst.page = nil
				go func(pg *rod.Page) { _ = pg.Close() }(pg)
			}
			delete(p.instances, name)
		}
	}
	p.mu.Unlock()

	if old != nil {
		go func() { _ = old.Close() }()
		log.Printf("[pool] profile/代理变化，浏览器将按新配置重建")
		go p.Prestart()
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	b := p.browser
	p.browser = nil
	for name, inst := range p.instances {
		if inst.page != nil {
			pg := inst.page
			inst.page = nil
			go func(pg *rod.Page) { _ = pg.Close() }(pg)
		}
		delete(p.instances, name)
	}
	p.mu.Unlock()
	if b != nil {
		_ = b.Close()
	}
}

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

// runChat 在独立 Tab 上执行一轮对话：导航 → 登录态检查 → 新对话 → 发消息 → 抓 SSE 流式回包。
func runChat(ctx context.Context, d platform.PlatformDriver, p *rod.Page, text string, emit func(string), extra proto.NetworkHeaders) error {
	// 聊天 Tab 需打开 Network 域才能收到 dataReceived
	if restore := p.EnableDomain(&proto.NetworkEnable{}); restore != nil {
		defer restore()
	}
	// 导入登录态时带回的外部请求头（如 X-CSRF / Referer），随聊天请求一并发出
	applyHeaders(p, extra)
	// 拦截图片/字体/媒体：聊天界面并不需要，能显著降低软渲染下的内存与 CPU
	if stopHard, err := blockHeavyResources(p); err == nil {
		defer stopHard()
	}
	tp := p.Timeout(20 * time.Second)
	if err := tp.Navigate(d.LoginURL()); err != nil {
		return fmt.Errorf("检查登录状态: %w", err)
	}
	tp.MustWaitLoad()
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

// blockHeavyResources 拦截 Image/Font/Media 请求（BlockedByClient），
// 聊天 Tab 用不到这些资源，省下软渲染解码的内存与带宽。
func blockHeavyResources(p *rod.Page) (stop func(), err error) {
	patterns := []*proto.FetchRequestPattern{
		{URLPattern: "*", ResourceType: proto.NetworkResourceTypeImage},
		{URLPattern: "*", ResourceType: proto.NetworkResourceTypeFont},
		{URLPattern: "*", ResourceType: proto.NetworkResourceTypeMedia},
	}
	if err := (proto.FetchEnable{Patterns: patterns}).Call(p); err != nil {
		return nil, err
	}
	stop = p.EachEvent(func(e *proto.FetchRequestPaused) {
		_ = proto.FetchFailRequest{RequestID: e.RequestID, ErrorReason: proto.NetworkErrorReasonBlockedByClient}.Call(p)
	})
	return stop, nil
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
