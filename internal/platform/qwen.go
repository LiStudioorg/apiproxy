package platform

import "github.com/go-rod/rod"

type qwenDriver struct{}

func init() { Register(&qwenDriver{}) }

func (d *qwenDriver) Name() string      { return "qwen" }
func (d *qwenDriver) LoginURL() string  { return "https://chat.qwen.ai" }
func (d *qwenDriver) StreamURL() string { return "/api/chat" }

func (d *qwenDriver) Selectors() Selectors {
	return Selectors{
		Input:   "textarea[placeholder*='输入'], div[contenteditable='true']",
		Send:    "button[type='submit']",
		NewChat: "div[role='button'][aria-label*='新对话']",
	}
}

func (d *qwenDriver) LoginCheck(page *rod.Page) (bool, error) {
	ok, _, err := page.Has(d.Selectors().Input)
	return ok, err
}

func (d *qwenDriver) ParseSSEChunk(raw []byte) (string, bool) {
	if IsDone(raw) {
		return "", true
	}
	delta, done := JSONDelta(raw)
	if done {
		return delta, true
	}
	return delta, false
}
