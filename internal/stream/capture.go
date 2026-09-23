package stream

import (
	"bytes"
	"strings"
)

// FrameDecoder 将 Network.dataReceived 的流式字节按 SSE 帧（\n\n 或 \r\n\r\n）切分。
// 兼容注释行、空行与截断边界；缓冲区设置上限防止异常增长。
type FrameDecoder struct {
	buf     []byte
	maxSize int
}

const defaultMaxFrameBuf = 4 << 20 // 4MB

func NewFrameDecoder() *FrameDecoder {
	return &FrameDecoder{maxSize: defaultMaxFrameBuf}
}

// Feed 追加字节并切出完整帧，逐帧回调 handle(payload)。
func (d *FrameDecoder) Feed(chunk []byte, handle func(payload []byte)) {
	if len(d.buf) == 0 && len(chunk) == 0 {
		return
	}
	if len(d.buf)+len(chunk) > d.maxSize {
		// 老平台一次性吐超大 payload 时丢弃最旧部分
		d.buf = d.buf[len(d.buf)+len(chunk)-d.maxSize:]
	}
	d.buf = append(d.buf, chunk...)
	for {
		idx := indexSep(d.buf)
		if idx < 0 {
			return
		}
		event := d.buf[:idx]
		d.buf = d.buf[idx:]
		d.buf = bytes.TrimPrefix(d.buf, []byte("\n\n"))
		d.buf = bytes.TrimPrefix(d.buf, []byte("\r\n\r\n"))
		for _, p := range dataPayloads(event) {
			if len(p) > 0 {
				handle(p)
			}
		}
	}
}

// Flush 处理流结束前残留在缓冲区中的最后内容（可能是无结尾空行的完整帧）。
func (d *FrameDecoder) Flush(handle func(payload []byte)) {
	if len(d.buf) == 0 {
		return
	}
	payloads := dataPayloads(d.buf)
	if len(payloads) == 0 {
		rest := bytes.TrimSpace(d.buf)
		if len(rest) > 0 {
			handle(rest)
		}
	} else {
		for _, p := range payloads {
			if len(p) > 0 {
				handle(p)
			}
		}
	}
	d.buf = nil
}

func indexSep(b []byte) int {
	if i := bytes.Index(b, []byte("\n\n")); i >= 0 {
		return i
	}
	return bytes.Index(b, []byte("\r\n\r\n"))
}

// dataPayloads 提取 SSE 事件中的所有 data: 行（忽略注释行 / event: / id: 等）。
// 同一事件的多行 data 按规范以 \n 连接为单条 payload。
func dataPayloads(event []byte) [][]byte {
	lines := bytes.Split(event, []byte("\n"))
	var out [][]byte
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := bytes.TrimSuffix(lines[i], []byte("\r"))
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == ':' {
			continue // 空行或注释（keep-alive）
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		v := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(cur) > 0 {
			cur = append(cur, '\n')
		}
		cur = append(cur, v...)
	}
	flush()
	return out
}

// MatchURL 判断请求 URL 是否命中流式端点关键词。
func MatchURL(rawURL, keyword string) bool {
	if keyword == "" {
		return true
	}
	return strings.Contains(rawURL, keyword)
}