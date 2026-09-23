package stream

import (
	"bytes"
	"strings"
)

type FrameDecoder struct {
	buf []byte
}

func (d *FrameDecoder) Feed(chunk []byte, handle func(payload []byte)) {
	d.buf = append(d.buf, chunk...)
	for {
		idx := bytes.Index(d.buf, []byte("\n\n"))
		if idx < 0 {
			return
		}
		event := d.buf[:idx]
		d.buf = d.buf[idx+2:]
		payloads := extractPayloads(event)
		if len(payloads) == 0 {
			payloads = [][]byte{bytes.TrimSpace(event)}
		}
		for _, p := range payloads {
			if len(p) > 0 {
				handle(p)
			}
		}
	}
}

func (d *FrameDecoder) Flush(handle func(payload []byte)) {
	rest := bytes.TrimSpace(d.buf)
	if len(rest) > 0 {
		handle(rest)
	}
	d.buf = nil
}

func extractPayloads(event []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimLeft(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(line) > 0 {
				out = append(out, line)
			}
		}
	}
	return out
}

func MatchURL(rawURL, keyword string) bool {
	if keyword == "" {
		return true
	}
	return strings.Contains(rawURL, keyword)
}
