package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
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

func WriteChunk(w http.ResponseWriter, buf *bufio.Writer, id, model string, delta map[string]interface{}, finish interface{}) error {
	chunk := api.NewChunk(id, model, delta, finish, 0)
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(buf, "data: %s\n\n", raw); err != nil {
		return err
	}
	return buf.Flush()
}

func WriteDone(w http.ResponseWriter, buf *bufio.Writer, id, model string) error {
	if _, err := buf.WriteString(doneLine); err != nil {
		return err
	}
	return buf.Flush()
}

func WriteEndChunk(w http.ResponseWriter, buf *bufio.Writer, id, model string, finishReason string) error {
	finish := api.NewChunk(id, model, nil, finishReason, 0)
	raw, _ := json.Marshal(finish)
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
