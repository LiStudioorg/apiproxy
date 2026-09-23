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
	"sync/atomic"
	"time"

	"web2api/api"
	"web2api/internal/auth"
	"web2api/internal/browser"
	"web2api/internal/config"
	"web2api/internal/platform"
	"web2api/internal/stream"
	"web2api/pkg/account"
	"web2api/pkg/queue"
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

type LogEvent struct {
	Time   time.Time `json:"time"`
	Model  string    `json:"model"`
	Kind   string    `json:"kind"` // request / login / system
	Status int       `json:"status"`
	MS     int64     `json:"ms"`
	Msg    string    `json:"msg"`
}

// LogSink 将日志实时推送到管理界面（admin 的 WS hub 实现）。
type LogSink interface {
	PushLog(ev LogEvent)
}

// Logs 请求日志环形缓冲。
type Logs struct {
	mu  sync.Mutex
	buf []LogEvent
	cap int
}

func NewLogs(capacity int) *Logs {
	if capacity <= 0 {
		capacity = 200
	}
	return &Logs{cap: capacity}
}

func (l *Logs) Add(ev LogEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, ev)
	if len(l.buf) > l.cap {
		l.buf = l.buf[len(l.buf)-l.cap:]
	}
}

func (l *Logs) Snapshot() []LogEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LogEvent, len(l.buf))
	copy(out, l.buf)
	return out
}

type Options struct {
	Sink     LogSink
	Queue    *queue.Queue
	Accounts map[string]*account.Pool
	Logs     *Logs
}

type Router struct {
	cfg     *config.Config
	pool    *browser.Pool
	limits  atomic.Value // map[string]*rate.Limiter，支持热更新
	stats   *Stats
	logs    *Logs
	apiKeys *auth.APIKeyAuth
	opts    Options
}

func New(cfg *config.Config, pool *browser.Pool, stats *Stats, apiKeys *auth.APIKeyAuth, opts Options) *Router {
	r := &Router{cfg: cfg, pool: pool, stats: stats, apiKeys: apiKeys, opts: opts, logs: opts.Logs}
	if r.logs == nil {
		r.logs = NewLogs(200)
	}
	r.rebuildLimiters()
	return r
}

func (r *Router) rebuildLimiters() {
	limiters := map[string]*rate.Limiter{}
	for _, d := range platform.All() {
		pc, ok := r.cfg.GetPlatform(d.Name())
		if !ok || !pc.Enabled {
			continue
		}
		limiters[d.Name()] = rate.New(pc.MaxConcurrent, pc.MinInterval, pc.RequestLimit)
	}
	if limiters == nil {
		limiters = map[string]*rate.Limiter{}
	}
	r.limits.Store(limiters)
}

// Reload 配置热更新后重建限流器（并发数/间隔/日配额即时生效）。
func (r *Router) Reload() {
	r.rebuildLimiters()
}

func (r *Router) limiter(name string) *rate.Limiter {
	m, _ := r.limits.Load().(map[string]*rate.Limiter)
	if m == nil {
		return nil
	}
	return m[name]
}

func (r *Router) Logs() *Logs { return r.logs }

func (r *Router) emitLog(ev LogEvent) {
	if r.logs == nil {
		return
	}
	r.logs.Add(ev)
	if r.opts.Sink != nil {
		r.opts.Sink.PushLog(ev)
	}
}

func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", r.requireKey(r.wrap(r.enqueue(r.handleChatCompletions))))
	mux.HandleFunc("/v1/models", r.requireKey(r.wrap(r.handleModels)))
	return mux
}

// enqueue 有界队列：队列满时立即 429；否则交给 worker 执行，
// handler 阻塞等待 worker 写完响应，保证流式输出正常冲刷。
func (r *Router) enqueue(next http.HandlerFunc) http.HandlerFunc {
	if r.opts.Queue == nil {
		return next
	}
	return func(w http.ResponseWriter, req *http.Request) {
		done := make(chan struct{})
		err := r.opts.Queue.Submit(func(ctx context.Context) error {
			defer close(done)
			next(w, req)
			return nil
		})
		if err != nil {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		<-done
	}
}

// requireKey 标准 Bearer Token 鉴权，防止局域网内其他人直接调用。
func (r *Router) requireKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.apiKeys == nil || r.apiKeys.Empty() {
			next(w, req)
			return
		}
		h := req.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || !r.apiKeys.Valid(strings.TrimPrefix(h, "Bearer ")) {
			writeError(w, http.StatusUnauthorized, "invalid or missing api key (Authorization: Bearer <key>)")
			return
		}
		next(w, req)
	}
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
	start := time.Now()
	emit := func(status int, msg string) {
		r.emitLog(LogEvent{Time: time.Now(), Model: cr.Model, Kind: "request", Status: status, MS: time.Since(start).Milliseconds(), Msg: msg})
	}
	fail := func(status int, msg string) {
		emit(status, msg)
		writeError(w, status, msg)
	}

	if len(cr.Messages) == 0 {
		fail(http.StatusBadRequest, "messages 不能为空")
		return
	}
	text := cr.Messages[len(cr.Messages)-1].Text()
	if strings.TrimSpace(text) == "" {
		fail(http.StatusBadRequest, "最后一条消息内容为空")
		return
	}

	driver, ok := platform.Get(cr.Model)
	if !ok {
		fail(http.StatusNotFound, "未知模型: "+cr.Model)
		return
	}

	lim := r.limiter(cr.Model)
	if lim == nil {
		fail(http.StatusServiceUnavailable, "模型未启用: "+cr.Model)
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Minute)
	defer cancel()

	if err := lim.Acquire(ctx); err != nil {
		if errorsIs(ctx, err) {
			return
		}
		fail(http.StatusTooManyRequests, err.Error())
		return
	}
	defer lim.Done()

	// 账号轮换：当日配额用尽则自动切到备用账号
	pc, _ := r.cfg.GetPlatform(cr.Model)
	if r.pool.ShouldRotate(cr.Model, pc.RequestLimit) {
		r.emitLog(LogEvent{Time: time.Now(), Model: cr.Model, Kind: "system", Status: 0, Msg: "当日配额用尽，自动切换备用账号"})
		if _, err := r.pool.Rotate(driver); err == nil {
			emit(0, "已切换备用账号")
		}
	}

	inst, err := r.pool.Ensure(driver)
	if err != nil {
		fail(http.StatusServiceUnavailable, err.Error())
		return
	}

	id := newID()
	prompt := buildPrompt(cr.Messages)
	markUsed := func() {
		if pool := r.opts.Accounts[cr.Model]; pool != nil {
			pool.MarkUsed()
		}
	}
	if cr.Stream {
		stream.WriteSSEHeaders(w)
		buf := bufio.NewWriter(w)
		f, ok := w.(http.Flusher)
		if !ok {
			fail(http.StatusInternalServerError, "不支持流式输出")
			return
		}
		_ = stream.WriteRoleChunk(w, buf, id, cr.Model)
		f.Flush()

		var sb strings.Builder
		streamErr := inst.Chat(ctx, prompt, func(delta string) {
			sb.WriteString(delta)
			_ = stream.WriteChunk(w, buf, id, cr.Model, map[string]interface{}{"content": delta}, nil)
			f.Flush()
		})
		if streamErr == nil {
			_ = stream.WriteFinishChunk(w, buf, id, cr.Model, api.NewUsage(
				api.EstimateTokens(prompt),
				api.EstimateTokens(sb.String()),
			))
			_ = stream.WriteDone(buf)
			r.stats.Add(cr.Model)
			markUsed()
			emit(200, "ok")
			return
		}
		// 流中途失败：以 SSE error 事件收尾，保证客户端不挂起
		_ = stream.WriteErrorEvent(w, buf, streamErr.Error())
		emit(502, streamErr.Error())
		return
	}

	var sb strings.Builder
	err = inst.Chat(ctx, prompt, func(delta string) {
		sb.WriteString(delta)
	})
	if err != nil {
		emit(502, err.Error())
		r.stats.Add(cr.Model)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	r.stats.Add(cr.Model)
	markUsed()

	usage := api.NewUsage(api.EstimateTokens(prompt), api.EstimateTokens(sb.String()))
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
		Usage: &usage,
	}
	writeJSON(w, http.StatusOK, resp)
	emit(200, "ok")
}

// buildPrompt 将多轮消息拼成单段提示词下发（保留 system 上下文，避免平台历史混乱）。
func buildPrompt(msgs []api.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var parts []string
	for _, m := range msgs {
		txt := strings.TrimSpace(m.Text())
		if txt == "" {
			continue
		}
		switch m.Role {
		case "system", "developer":
			parts = append(parts, "[system]\n"+txt)
		case "assistant":
			parts = append(parts, "[assistant]\n"+txt)
		default:
			parts = append(parts, "[user]\n"+txt)
		}
	}
	return strings.Join(parts, "\n\n")
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
