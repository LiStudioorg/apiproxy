package stream

import (
	"bufio"
	"encoding/json"
	"net/http"

	"web2api/api"
)

const doneLine = "data: [DONE]\n\n"

func WriteSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

func writeData(buf *bufio.Writer, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := buf.WriteString("data: "); err != nil {
		return err
	}
	if _, err := buf.Write(raw); err != nil {
		return err
	}
	if _, err := buf.WriteString("\n\n"); err != nil {
		return err
	}
	return buf.Flush()
}

// WriteRoleChunk 首包：携带 role=assistant，供客户端初始化对话角色。
func WriteRoleChunk(w http.ResponseWriter, buf *bufio.Writer, id, model string) error {
	return writeData(buf, api.NewChunk(id, model, map[string]interface{}{"role": "assistant"}, nil, 0))
}

func WriteChunk(w http.ResponseWriter, buf *bufio.Writer, id, model string, delta map[string]interface{}, finish interface{}) error {
	return writeData(buf, api.NewChunk(id, model, delta, finish, 0))
}

// WriteFinishChunk 收尾包：finish_reason=stop + 累计 usage。
func WriteFinishChunk(w http.ResponseWriter, buf *bufio.Writer, id, model string, usage api.Usage) error {
	return writeData(buf, api.NewChunkWithUsage(id, model, nil, "stop", 0, &usage))
}

// WriteDone 输出 data: [DONE]
func WriteDone(buf *bufio.Writer) error {
	_, err := buf.WriteString(doneLine)
	if err != nil {
		return err
	}
	return buf.Flush()
}

// WriteErrorEvent 流已经开始后出错时，用 OpenAI 兼容的 SSE error 事件上报后再收尾。
func WriteErrorEvent(w http.ResponseWriter, buf *bufio.Writer, msg string) error {
	if err := writeData(buf, api.NewError(msg, "upstream_error", "")); err != nil {
		return err
	}
	_, werr := buf.WriteString(doneLine)
	if werr != nil {
		return werr
	}
	return buf.Flush()
}

// WriteKeepAlive 输出 SSE 注释行，防止代理长时间无内容而超时断流。
func WriteKeepAlive(buf *bufio.Writer, w http.Flusher) {
	_, _ = buf.WriteString(": keep-alive\n\n")
	_ = buf.Flush()
	w.Flush()
}