package browser

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/ysmood/gson"
)

// importedHeaderBlocklist 浏览器会自动生成/管理的头，导入 cURL 时回灌反而出错。
var importedHeaderBlocklist = map[string]bool{
	"host": true, "content-length": true, "accept-encoding": true,
	"connection": true, "user-agent": true, "cookie": true,
}

// parseImportText 解析「复制为 cURL」整段，或裸 Cookie 行；返回 cookie 字符串与额外请求头。
// 每家平台头不一样（X-CSRF / Referer / Origin / Authorization 等），cURL 里带的都保留。
func parseImportText(text string) (cookieLine string, headers proto.NetworkHeaders, err error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", nil, errors.New("内容为空")
	}
	headers = proto.NetworkHeaders{}
	if strings.HasPrefix(trimmed, "curl") {
		re := regexp.MustCompile(`(?i)(?:-H|--header)\s+['"]([^'"]+)['"]`)
		for _, m := range re.FindAllStringSubmatch(trimmed, -1) {
			hv := strings.TrimSpace(m[1])
			idx := strings.Index(hv, ":")
			if idx <= 0 {
				continue
			}
			name := strings.TrimSpace(hv[:idx])
			val := strings.TrimSpace(hv[idx+1:])
			if strings.EqualFold(name, "cookie") {
				cookieLine = val
				continue
			}
			if importedHeaderBlocklist[strings.ToLower(name)] {
				continue
			}
			headers[name] = gson.New(val)
		}
		if cookieLine == "" {
			re2 := regexp.MustCompile(`(?i)(?:--cookie|-b)\s+['"]([^'"]+)['"]`)
			if m := re2.FindStringSubmatch(trimmed); len(m) > 1 {
				cookieLine = m[1]
			}
		}
	} else {
		cookieLine = trimmed
	}
	return strings.TrimSpace(cookieLine), headers, nil
}

// parseCookies 把 "k=v; k2=v2" 拆成 Cookie 项；自动跳过 cURL 误带的属性字段。
func parseCookies(line string) []cookieItem {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "Cookie:")
	line = strings.TrimPrefix(line, "cookie:")
	var out []cookieItem
	for _, part := range strings.Split(line, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		name := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if name == "" || val == "" {
			continue
		}
		switch strings.ToLower(name) {
		case "path", "domain", "expires", "samesite", "secure", "httponly", "max-age", "priority":
			continue
		default:
			out = append(out, cookieItem{Name: name, Value: val})
		}
	}
	return out
}

type cookieItem struct {
	Name  string
	Value string
}

// ImportSession 把外部会话（电脑浏览器导出的 Cookie + 必要请求头）注入常驻无头浏览器，
// 然后后台加载一次平台首页，让站点自己的 JS 恢复 localStorage 等其余状态，最后 LoginCheck 验证。
// 不落地任何 Cookie/接头到磁盘或日志（仅运行时注入 profile）。
func (inst *Instance) ImportSession(text string) error {
	if inst == nil {
		return errors.New("实例不存在")
	}
	cookieLine, headers, err := parseImportText(text)
	if err != nil {
		return err
	}
	cookies := parseCookies(cookieLine)
	if len(cookies) == 0 {
		return errors.New("没有解析出任何 Cookie：请从电脑浏览器 DevTools → Network → 右键请求 → 复制为 cURL 整段粘贴")
	}
	u, err := url.Parse(inst.driver.LoginURL())
	if err != nil {
		return fmt.Errorf("解析平台地址: %w", err)
	}
	host := u.Hostname()

	b, err := inst.pool.ensureBrowser()
	if err != nil {
		return err
	}
	// 额外请求头存在实例上，供后续每个聊天 Tab 复用（浏览器自动生成的头我们不覆盖）
	inst.pool.mu.Lock()
	inst.extraHeaders = headers
	inst.pool.mu.Unlock()

	tmp := stealth.MustPage(b)
	defer func() { _ = tmp.Close() }()
	// 注意：这里只用「单次命令式」CDP 开关，不启用基于订阅的 helper（blockHeavyResources /
	// EnableDomain+restore 那套 EachEvent 机制在持久浏览器多 Tab 并发下会在 teardown 死锁）。
	// 验证页仅一次性使用，无需资源拦截。
	_ = proto.NetworkEnable{}.Call(tmp)
	for _, c := range cookies {
		_, _ = proto.NetworkSetCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   "." + host,
			Path:     "/",
			Secure:   true,
			SameSite: proto.NetworkCookieSameSiteLax,
		}.Call(tmp)
	}
	if len(headers) > 0 {
		_ = proto.NetworkSetExtraHTTPHeaders{Headers: headers}.Call(tmp)
	}
	tp := tmp.Timeout(15 * time.Second)
	if err := tp.Navigate(inst.driver.LoginURL()); err != nil {
		return fmt.Errorf("导航验证: %w", err)
	}
	// 加载等待非关键：部分平台首页是重型 SPA，load 事件慢，失败也继续往下验证
	_ = tp.Timeout(10 * time.Second).WaitLoad()
	time.Sleep(600 * time.Millisecond) // 等站点启动 JS 恢复会话状态

	loggedIn, cerr := inst.driver.LoginCheck(tmp.Timeout(12 * time.Second))
	inst.pool.mu.Lock()
	if cerr == nil {
		inst.logged = &loggedIn
	} else {
		v := false
		inst.logged = &v
	}
	inst.pool.mu.Unlock()
	if cerr != nil {
		return fmt.Errorf("Cookie 已注入，但该平台登录态检测失败: %v", cerr)
	}
	if !loggedIn {
		return errors.New("Cookie 已注入但平台判定尚未登录（部分平台需重新扫码，或还需把 localStorage 一并导入）")
	}
	return nil
}

// extraHeaders 返回实例保存的外部请求头（读锁内复制）。
func (inst *Instance) extraHeadersSnapshot() proto.NetworkHeaders {
	inst.pool.mu.Lock()
	defer inst.pool.mu.Unlock()
	if len(inst.extraHeaders) == 0 {
		return nil
	}
	out := proto.NetworkHeaders{}
	for k, v := range inst.extraHeaders {
		out[k] = v
	}
	return out
}

// applyHeaders 把外部请求头应用到页面（网络域已启用时调用）。
func applyHeaders(p *rod.Page, headers proto.NetworkHeaders) {
	if len(headers) == 0 {
		return
	}
	_ = proto.NetworkSetExtraHTTPHeaders{Headers: headers}.Call(p)
}
