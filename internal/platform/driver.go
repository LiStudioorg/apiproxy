package platform

import "github.com/go-rod/rod"

type Selectors struct {
	Input   string
	Send    string
	NewChat string
}

type PlatformDriver interface {
	Name() string
	LoginURL() string
	LoginCheck(page *rod.Page) (bool, error)
	StreamURL() string
	Selectors() Selectors
	// ParseSSEChunk 解析单条 SSE payload：
	// 返回增量文本 delta、是否结束 done、以及（可选）上游错误 err。
	ParseSSEChunk(raw []byte) (delta string, done bool, err error)
}
