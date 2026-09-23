package router

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"web2api/api"
	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/stream"
	"web2api/pkg/rate"
)

type Stats struct {
	mu      sync.Mutex
	today   map[string]int
	lastDay string
}

func NewStats() *Stats {
	return &Stats{
		today:   map[string]int{},
		lastDay: time.Now().Format("2006-01-02"),
	}
}

func (s *Stats) Add(platform string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if day != s.lastDay {
		s.today = map[string]int{}
		s.lastDay = day
	}
	s.today[platform]++
}

func (s *Stats) Snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.today))
	for k, v := range s.today {
		out[k] = v
	}
	return out
}

type Router struct {
	cfg      *config.Config
	pool     *browser.Pool
	limiters map[string]*rate.Limiter
	stats    *Stats
}

func New(cfg *config.Config, pool *browser.Pool, stats *Stats) *Router {
	limiters := map[string]*rate.Limiter{}
	for _, d := range platform.All() {
		pc, ok := cfg.GetPlatform(d.Name())
		if !ok || !pc.Enabled {
			continue
		}
		limiters[d.Name()] = rate.New(pc.MaxConcurrent, pc.MinInterval, pc.RequestLimit)
	}
	return &Router{cfg: cfg, pool: pool, limiters: limiters, stats: stats}
}

func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", r.wrap(r.handleChatCompletions))
	mux.HandleFunc("/v1/models", r.wrap(r.handleModels))
	return mux
}

func (r *Router) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[panic] %v", rec)
				writeError(w, http.StatusInternalServerError, "内部错误")
			}
		}()
		h(w, req)
	}
}

func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list := api.ModelList{Object: "list"}
	for _, d := range platform.All() {
		pc, ok := r.cfg.GetPlatform(d.Name())
		if !ok || !pc.Enabled {
			continue
		}
		list.Data = append(list.Data, api.ModelInfo{
			ID:      d.Name(),
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "web2api",
			Allow:   []string{"chat"},
		})
	}
	writeJSON(w, http.StatusOK, list)
}

func (r *Router) handleChatCompletions(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var cr api.ChatRequest
	if err := json.NewDecoder(req.Body).Decode(&cr); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	if cr.Model == "" {
		writeError(w, http.StatusBadRequest, "缺少 model 字段")
		return
	}
	if len(cr.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages 不能为空")
		return
	}
	text := cr.Messages[len(cr.Messages)-1].Text()
	if strings.TrimSpace(text) == "" {
		writeError(w, http.StatusBadRequest, "最后一条消息内容为空")
		return
	}

	driver, ok := platform.Get(cr.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "未知模型: "+cr.Model)
		return
	}

	lim := r.limiters[cr.Model]
	if lim == nil {
		writeError(w, http.StatusServiceUnavailable, "模型未启用: "+cr.Model)
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Minute)
	defer cancel()

	if err := lim.Acquire(ctx); err != nil {
		if errorsIs(ctx, err) {
			return
		}
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer lim.Done()

	inst, err := r.pool.Ensure(driver)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	id := newID()
	if cr.Stream {
		stream.WriteSSEHeaders(w)
		buf := bufio.NewWriter(w)
		f, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "不支持流式输出")
			return
		}
		err := inst.Chat(ctx, text, func(delta string) {
			_ = stream.WriteChunk(w, buf, id, cr.Model, map[string]interface{}{"content": delta}, nil)
			f.Flush()
		})
		if err == nil {
			_ = stream.WriteEndChunk(w, buf, id, cr.Model, "stop")
		}
		_ = stream.WriteDone(w, buf, id, cr.Model)
		r.stats.Add(cr.Model)
		return
	}

	var sb strings.Builder
	err = inst.Chat(ctx, text, func(delta string) {
		sb.WriteString(delta)
	})
	r.stats.Add(cr.Model)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	resp := api.ChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   cr.Model,
		Choices: []api.Choice{{
			Index:        0,
			Message:      api.Message{Role: "assistant", Content: sb.String()},
			FinishReason: "stop",
		}},
	}
	writeJSON(w, http.StatusOK, resp)
}

func errorsIs(ctx context.Context, err error) bool {
	return err == context.DeadlineExceeded || err == context.Canceled
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, api.NewError(message, "error", ""))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
