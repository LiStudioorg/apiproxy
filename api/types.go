package api

import (
	"fmt"
	"time"
)

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	MaxTokens   *int      `json:"max_tokens"`
	Temperature *float64  `json:"temperature"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (m Message) Text() string {
	return m.Content
}

func (r *ChatRequest) UserText() string {
	if len(r.Messages) == 0 {
		return ""
	}
	return fmt.Sprintf("%s", r.Messages[len(r.Messages)-1].Text())
}

type ErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func NewError(message, etype, code string) ErrorResponse {
	var e ErrorResponse
	e.Error.Message = message
	e.Error.Type = etype
	e.Error.Code = code
	return e
}

type Choice struct {
	Index        int         `json:"index"`
	Message      Message     `json:"message"`
	FinishReason interface{} `json:"finish_reason"`
}

type ChatCompletion struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

type ChunkChoice struct {
	Index        int                    `json:"index"`
	Delta        map[string]interface{} `json:"delta"`
	FinishReason interface{}            `json:"finish_reason"`
}

type ChatStreamChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

func NewChunk(id, model string, delta map[string]interface{}, finish interface{}, index int) ChatStreamChunk {
	return ChatStreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChunkChoice{{
			Index:        index,
			Delta:        delta,
			FinishReason: finish,
		}},
	}
}

type ModelInfo struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	OwnedBy string   `json:"owned_by"`
	Allow   []string `json:"allow"`
}

type ModelList struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}
