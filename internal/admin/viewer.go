package admin

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/gorilla/websocket"

	"web2api/internal/platform"
)

// handleViewerWS 内嵌浏览器画面：headless 环境下无需桌面即可完成登录。
// 服务端以二进制帧（JPEG 截图）持续推送页面，客户端以 JSON 回传鼠标/滚动/输入事件。
func (h *Handler) handleViewerWS(w http.ResponseWriter, r *http.Request) {
	if !h.auth.Valid(r) {
		writeErrCode(w, http.StatusUnauthorized, "未登录或会话已过期")
		return
	}
	name := r.URL.Query().Get("platform")
	d, ok := platform.Get(name)
	if !ok {
		writeErrCode(w, http.StatusBadRequest, "未知平台: "+name)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	inst, err := h.pool.OpenViewer(d)
	if err != nil {
		_ = conn.WriteJSON(map[string]interface{}{"type": "error", "msg": err.Error()})
		return
	}
	// 画面结束后关闭该平台的登录画面 Tab，释放渲染进程
	defer h.pool.CloseView(name)
	go h.hub.PushStatus(h.snapshot())

	viewer := &browserViewer{conn: conn, page: inst.Page()}
	viewer.serve()
}

// browserViewer 管理一个内嵌浏览器会话的截图推送与事件注入。
type browserViewer struct {
	conn *websocket.Conn
	page proto.Client
	mu   sync.Mutex
}

func (v *browserViewer) serve() {
	done := make(chan struct{})

	go func() {
		t := time.NewTicker(450 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				img, err := v.capture()
				if err != nil {
					_ = v.conn.WriteJSON(map[string]interface{}{"type": "error", "msg": "浏览器已退出: " + err.Error()})
					_ = v.conn.Close()
					return
				}
				if err := v.conn.WriteMessage(websocket.BinaryMessage, img); err != nil {
					_ = v.conn.Close()
					return
				}
			}
		}
	}()
	defer close(done)

	for {
		_, raw, err := v.conn.ReadMessage()
		if err != nil {
			return
		}
		var ev struct {
			Type   string  `json:"type"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			DeltaY int     `json:"delta_y"`
			Text   string  `json:"text"`
			Key    string  `json:"key"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		v.dispatch(ev)
	}
}

func (v *browserViewer) capture() ([]byte, error) {
	q := 55
	v.mu.Lock()
	defer v.mu.Unlock()
	res, err := proto.PageCaptureScreenshot{
		Format:                proto.PageCaptureScreenshotFormatJpeg,
		Quality:               &q,
		CaptureBeyondViewport: false,
		FromSurface:           true,
	}.Call(v.page)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

func (v *browserViewer) dispatch(ev struct {
	Type   string  `json:"type"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	DeltaY int     `json:"delta_y"`
	Text   string  `json:"text"`
	Key    string  `json:"key"`
}) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch ev.Type {
	case "move":
		_ = proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: ev.X, Y: ev.Y, Button: proto.InputMouseButtonNone}.Call(v.page)
	case "click":
		_ = proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: ev.X, Y: ev.Y, Button: proto.InputMouseButtonLeft, ClickCount: 1}.Call(v.page)
		_ = proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: ev.X, Y: ev.Y, Button: proto.InputMouseButtonLeft, ClickCount: 1}.Call(v.page)
	case "scroll":
		_ = proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel, X: ev.X, Y: ev.Y, DeltaY: float64(ev.DeltaY)}.Call(v.page)
	case "type":
		_ = proto.InputInsertText{Text: ev.Text}.Call(v.page)
	case "enter":
		_ = proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: "Enter", Code: "Enter"}.Call(v.page)
		_ = proto.InputInsertText{Text: "\r"}.Call(v.page)
		_ = proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyUp, Key: "Enter", Code: "Enter"}.Call(v.page)
	}
}
