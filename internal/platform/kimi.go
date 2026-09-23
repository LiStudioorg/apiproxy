package platform

import (
	"errors"

	"github.com/go-rod/rod"
)

type kimiDriver struct{}

func init() { Register(&kimiDriver{}) }

func (d *kimiDriver) Name() string      { return "kimi" }
func (d *kimiDriver) LoginURL() string  { return "https://www.kimi.com" }
func (d *kimiDriver) StreamURL() string { return "/api/chat" }

func (d *kimiDriver) Selectors() Selectors {
	return Selectors{
		Input:   "div[contenteditable='true']",
		Send:    "button[data-testid='send']",
		NewChat: "div[role='button'][aria-label*='新对话']",
	}
}

func (d *kimiDriver) LoginCheck(page *rod.Page) (bool, error) {
	ok, _, err := page.Has(d.Selectors().Input)
	return ok, err
}

func (d *kimiDriver) ParseSSEChunk(raw []byte) (string, bool, error) {
	if IsDone(raw) {
		return "", true, nil
	}
	if msg, ok := IsUpstreamError(raw); ok {
		return "", true, errors.New(msg)
	}
	delta, done := JSONDelta(raw)
	return delta, done, nil
}
