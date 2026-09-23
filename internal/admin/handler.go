package admin

import (
	"embed"
	"encoding/json"
	"net/http"
	"strings"

	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/router"
)

//go:embed static
var staticFS embed.FS

type Handler struct {
	cfg   *config.Config
	pool  *browser.Pool
	stats *router.Stats
}

func New(cfg *config.Config, pool *browser.Pool, stats *router.Stats) *Handler {
	return &Handler{cfg: cfg, pool: pool, stats: stats}
}

func (h *Handler) Register(mux *http.ServeMux, path string) {
	if path == "" {
		path = "/admin"
	}
	mux.HandleFunc(path, h.index)
	mux.HandleFunc(path+"/", h.index)
	mux.HandleFunc(path+"/api/status", h.status)
	mux.HandleFunc(path+"/api/refresh/", h.refresh)
	mux.HandleFunc(path+"/api/login/", h.login)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type PlatformStatus struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	LoggedIn      *bool  `json:"logged_in"`
	Running       bool   `json:"running"`
	TodayRequests int    `json:"today_requests"`
	ProfileDir    string `json:"profile_dir"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Platforms []PlatformStatus `json:"platforms"`
	}{}
	for _, d := range platform.All() {
		pc, ok := h.cfg.GetPlatform(d.Name())
		ps := PlatformStatus{Name: d.Name(), ProfileDir: pc.ProfileDir, Enabled: ok && pc.Enabled}
		if inst, err := h.pool.Peek(d); err == nil && inst != nil {
			ps.Running = true
			loggedIn, _ := inst.LoginStatus()
			ps.LoggedIn = &loggedIn
		}
		if ok {
			ps.TodayRequests = h.stats.Snapshot()[d.Name()]
		}
		out.Platforms = append(out.Platforms, ps)
	}
	writeJSON(w, out)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/api/refresh/")
	d, ok := platform.Get(name)
	if !ok {
		writeErr(w, "未知平台: "+name)
		return
	}
	inst, err := h.pool.Ensure(d)
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	loggedIn, err := inst.LoginStatus()
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"logged_in": loggedIn})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/api/login/")
	d, ok := platform.Get(name)
	if !ok {
		writeErr(w, "未知平台: "+name)
		return
	}
	inst, err := h.pool.Ensure(d)
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	if err := inst.Navigate(d.LoginURL()); err != nil {
		writeErr(w, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "url": d.LoginURL()})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}
