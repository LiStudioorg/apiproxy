package admin

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"web2api/internal/auth"
	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/router"
)

//go:embed web
var webFS embed.FS

const sessionCookie = "ws_session"

type Auth struct {
	store   *auth.SessionStore
	enabled bool
	hashes  map[string]bool
}

func NewAuth(enabled bool, ttl time.Duration, pwHash string) *Auth {
	a := &Auth{
		store:   auth.NewSessionStore(ttl),
		enabled: enabled,
		hashes:  map[string]bool{},
	}
	if pwHash != "" {
		a.hashes[pwHash] = true
	}
	return a
}

func (a *Auth) Enabled() bool { return a.enabled }

func (a *Auth) Verify(password string) bool {
	return a.enabled && a.hashes[auth.Hash(password)]
}

func (a *Auth) Issue(w http.ResponseWriter) {
	token := a.store.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) Valid(r *http.Request) bool {
	if !a.enabled {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.store.Valid(c.Value)
}

func (a *Auth) Clear(w http.ResponseWriter) {
	a.store.RevokeAll()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
}

type Handler struct {
	cfg       *config.Config
	pool      *browser.Pool
	stats     *router.Stats
	logs      *router.Logs
	auth      *Auth
	apiKeys   *auth.APIKeyAuth
	hub       *Hub
	loginOnce sync.Map
	onSaved   func() // 设置保存成功后的回调（账号池/浏览器/限流器重建）
}

func New(cfg *config.Config, pool *browser.Pool, stats *router.Stats, logs *router.Logs, a *Auth, apiKeys *auth.APIKeyAuth) *Handler {
	h := &Handler{cfg: cfg, pool: pool, stats: stats, logs: logs, auth: a, apiKeys: apiKeys}
	h.hub = NewHub()
	return h
}

// SetReloadHook 注入设置保存成功后的热更新逻辑。
func (h *Handler) SetReloadHook(fn func()) {
	h.onSaved = fn
}

// PushLog 实现 router.LogSink：把 /v1 请求日志实时广播到管理界面。
func (h *Handler) PushLog(ev router.LogEvent) {
	h.hub.PushLog(ev)
}

func (h *Handler) Routes(mux *http.ServeMux) {
	fsys, _ := fs.Sub(webFS, "web")
	mux.Handle("/assets/", http.FileServer(http.FS(fsys)))

	mux.HandleFunc("/", h.serveSPA)

	mux.HandleFunc("/api/login", h.handlePasswordLogin)
	mux.HandleFunc("/api/logout", h.requireAuth(h.handleLogout))
	mux.Handle("/api/status", h.requireAuth(h.handleStatus))
	mux.Handle("/api/logs", h.requireAuth(h.handleLogs))
	mux.Handle("/api/settings", h.requireAuth(h.handleSettings))
	mux.HandleFunc("/api/ws/status", h.wsStatus)
	mux.HandleFunc("/api/viewer/ws", h.handleViewerWS)
	mux.HandleFunc("/api/platform/login/", h.requireAuth(h.handlePlatformLogin))
	mux.HandleFunc("/api/platform/checklogin/", h.requireAuth(h.handleCheckLogin))
}

// serveSPA 根路径直接给出管理界面；旧 /admin 路径 302 重定向回根路径。
func (h *Handler) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/admin") || strings.HasPrefix(r.URL.Path, "/admin/") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.auth.Valid(r) {
			writeErrCode(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		next(w, r)
	}
}

func (h *Handler) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrCode(w, http.StatusMethodNotAllowed, "")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !h.auth.Verify(body.Password) {
		writeErrCode(w, http.StatusUnauthorized, "密码错误")
		return
	}
	h.auth.Issue(w)
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (h *Handler) handleLogout(w http.ResponseWriter, _ *http.Request) {
	h.auth.Clear(w)
	writeJSON(w, map[string]interface{}{"ok": true})
}

type PlatformStatus struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	LoggedIn      *bool  `json:"logged_in"`
	Running       bool   `json:"running"`
	TodayRequests int    `json:"today_requests"`
}

func (h *Handler) snapshot() []PlatformStatus {
	var out []PlatformStatus
	for _, d := range platform.All() {
		pc, ok := h.cfg.GetPlatform(d.Name())
		ps := PlatformStatus{Name: d.Name(), Enabled: ok && pc.Enabled}
		if inst, err := h.pool.Peek(d); err == nil && inst != nil {
			ps.Running = true
			loggedIn, lerr := inst.LoginStatus()
			if lerr == nil {
				ps.LoggedIn = &loggedIn
			}
		}
		if ok {
			ps.TodayRequests = h.stats.Snapshot()[d.Name()]
		}
		out = append(out, ps)
	}
	return out
}

func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"platforms": h.snapshot(),
		"auth":      h.auth.Enabled(),
		"time":      time.Now().Unix(),
	})
}

func (h *Handler) handleLogs(w http.ResponseWriter, _ *http.Request) {
	logs := []router.LogEvent{}
	if h.logs != nil {
		logs = h.logs.Snapshot()
	}
	writeJSON(w, map[string]interface{}{"logs": logs})
}

// handleSettings GET 返回当前设置快照；POST 保存并触发热更新（写入 config.toml，回读后重建账号池/浏览器/限流器）。
func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, h.cfg.SettingsSnapshot())
	case http.MethodPost:
		var body struct {
			Server    *config.ServerEdit                 `json:"server"`
			Platforms map[string]config.PlatformSettings `json:"platforms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErrCode(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
			return
		}
		if err := h.cfg.UpdateSettings(body.Server, body.Platforms); err != nil {
			writeErrCode(w, http.StatusBadRequest, err.Error())
			return
		}
		if h.onSaved != nil {
			h.onSaved()
		}
		go h.hub.PushStatus(h.snapshot())
		writeJSON(w, map[string]interface{}{"ok": true, "settings": h.cfg.SettingsSnapshot()})
	default:
		writeErrCode(w, http.StatusMethodNotAllowed, "")
	}
}

// 触发一次即视为打开过登录页——用于后续登录成功后的消息去重。
func (h *Handler) handlePlatformLogin(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/platform/login/")
	d, ok := platform.Get(name)
	if !ok {
		writeErrCode(w, http.StatusBadRequest, "未知平台: "+name)
		return
	}
	go h.hub.PushStatus(h.snapshot())
	h.loginOnce.Store(name, true)
	inst, err := h.pool.OpenLogin(d)
	if err != nil {
		h.loginOnce.Delete(name)
		writeErrCode(w, http.StatusBadRequest, err.Error())
		return
	}
	loggedIn, _ := inst.LoginStatus()
	go h.hub.PushStatus(h.snapshot())
	writeJSON(w, map[string]interface{}{
		"ok":        true,
		"url":       d.LoginURL(),
		"logged_in": loggedIn,
	})
}

func (h *Handler) handleCheckLogin(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/platform/checklogin/")
	d, ok := platform.Get(name)
	if !ok {
		writeErrCode(w, http.StatusBadRequest, "未知平台: "+name)
		return
	}
	inst, err := h.pool.Peek(d)
	if err != nil || inst == nil {
		writeJSON(w, map[string]interface{}{"logged_in": false, "running": false})
		return
	}
	loggedIn, lerr := inst.LoginStatus()
	first := false
	if loggedIn {
		if v, loaded := h.loginOnce.LoadAndDelete(name); loaded && v == true {
			first = true
			go h.hub.PushStatus(h.snapshot())
		}
	}
	if lerr != nil {
		writeErrCode(w, http.StatusInternalServerError, lerr.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"logged_in": loggedIn, "running": true, "first": first})
}

// wsStatus 实时推送：客户端连上即收到快照，之后任何平台状态变更都会广播。
func (h *Handler) wsStatus(w http.ResponseWriter, r *http.Request) {
	if !h.auth.Valid(r) {
		writeErrCode(w, http.StatusUnauthorized, "未登录或会话已过期")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	sub := h.hub.Subscribe()
	defer h.hub.Unsubscribe(sub)
	conn.WriteJSON(map[string]interface{}{"type": "status", "payload": h.snapshot()})
	go func() {
		for msg := range sub.Ch {
			if err := conn.WriteJSON(msg); err != nil {
				conn.Close()
				return
			}
		}
	}()

	var buf [64]byte
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			conn.Close()
			return
		}
		_ = buf
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		var _ = err
	}
}

func writeErrCode(w http.ResponseWriter, code int, msg string) {
	if code == 0 {
		code = http.StatusBadRequest
	}
	if msg == "" {
		msg = http.StatusText(code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}
