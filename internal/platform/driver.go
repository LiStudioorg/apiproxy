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
	ParseSSEChunk(raw []byte) (delta string, done bool)
}
