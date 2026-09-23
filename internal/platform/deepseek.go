package platform

import (
	"errors"

	"github.com/go-rod/rod"
)

type deepseekDriver struct{}

func init() { Register(&deepseekDriver{}) }

func (d *deepseekDriver) Name() string      { return "deepseek" }
func (d *deepseekDriver) LoginURL() string  { return "https://chat.deepseek.com" }
func (d *deepseekDriver) StreamURL() string { return "/api/v0/chat/completion" }

func (d *deepseekDriver) Selectors() Selectors {
	return Selectors{
		Input:   "textarea#chat-input",
		Send:    "div[role='button'][aria-label='发送']",
		NewChat: "div[role='button'][aria-label*='新对话']",
	}
}

func (d *deepseekDriver) LoginCheck(page *rod.Page) (bool, error) {
	ok, _, err := page.Has(d.Selectors().Input)
	return ok, err
}

func (d *deepseekDriver) ParseSSEChunk(raw []byte) (string, bool, error) {
	if IsDone(raw) {
		return "", true, nil
	}
	if msg, ok := IsUpstreamError(raw); ok {
		return "", true, errors.New(msg)
	}
	delta, done := JSONDelta(raw)
	return delta, done, nil
}
