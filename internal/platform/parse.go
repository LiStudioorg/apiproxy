package platform

import "encoding/json"

// JSONDelta 从各平台 SSE payload 中抽取增量文本与结束标志。
// 兼容主流格式：choices[].delta.content / reasoning_content、choices[].messages[]、
// content / text / delta 字符串、choices[].delta.content 为字符串数组、text_delta 事件。
func JSONDelta(payload []byte) (delta string, done bool) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(payload, &node); err != nil {
		return "", false
	}

	// 显式结束标记
	if raw, ok := node["type"]; ok {
		var t string
		if json.Unmarshal(raw, &t) == nil {
			switch t {
			case "done", "finished", "finish", "end", "error", "failed":
				return "", true
			}
		}
	}
	if raw, ok := node["finish_reason"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return "", true
		}
	}
	if raw, ok := node["is_completion"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil && b {
			return "", true
		}
	}

	// choices[...]
	if raw, ok := node["choices"]; ok {
		var choices []struct {
			Index         int     `json:"index"`
			Delta         *struct {
				Content         any     `json:"content"`
				Reasoning       any     `json:"reasoning_content"`
				ReasoningDetail *string `json:"reasoning_content_detail"`
			} `json:"delta"`
			Message     *struct {
				Content any `json:"content"`
			} `json:"message"`
			Messages []struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
			} `json:"messages"`
			FinishReason *string `json:"finish_reason"`
		}
		if err := json.Unmarshal(raw, &choices); err == nil {
			for _, c := range choices {
				// 兜底结束：finish_reason 出现且无内容
				if c.FinishReason != nil && *c.FinishReason != "" &&
					c.Delta == nil && c.Message == nil && len(c.Messages) == 0 {
					return "", true
				}
				if c.Delta != nil {
					if txt := anyToString(c.Delta.Reasoning); txt != "" {
						return txt, false
					}
					if txt := anyToString(c.Delta.Content); txt != "" {
						return txt, false
					}
					if c.Delta.ReasoningDetail != nil && *c.Delta.ReasoningDetail != "" {
						return *c.Delta.ReasoningDetail, false
					}
				}
				if c.Message != nil {
					if txt := anyToString(c.Message.Content); txt != "" {
						return txt, false
					}
				}
				for _, m := range c.Messages {
					if m.Content != nil && *m.Content != "" {
						return *m.Content, false
					}
				}
			}
		}
	}

	// 顶层快捷字段
	for _, key := range []string{"content", "text", "delta"} {
		if raw, ok := node[key]; ok {
			if txt := anyToString(raw); txt != "" {
				return txt, false
			}
		}
	}
	if raw, ok := node["response"]; ok {
		if txt := anyToString(raw); txt != "" {
			return txt, false
		}
	}

	return "", false
}

// anyToString 兼容 string / []string / []any / json.RawMessage / 空值。
func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.RawMessage:
		var inner any
		if json.Unmarshal(t, &inner) == nil {
			return anyToString(inner)
		}
	case []string:
		out := ""
		for _, s := range t {
			out += s
		}
		return out
	case []any:
		out := ""
		for _, item := range t {
			if s, ok := item.(string); ok {
				out += s
			}
		}
		return out
	}
	return ""
}

// IsDone 判断 data 是否为 [DONE]。
func IsDone(payload []byte) bool {
	s := string(payload)
	return s == "[DONE]" || s == "[done]"
}

// IsUpstreamError 判断 payload 是否为平台错误事件，返回错误信息。
func IsUpstreamError(payload []byte) (string, bool) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(payload, &node); err != nil {
		return "", false
	}
	if raw, ok := node["type"]; ok {
		var t string
		if json.Unmarshal(raw, &t) == nil && (t == "error" || t == "failed") {
			msg := ""
			for _, k := range []string{"message", "msg", "error"} {
				if r, ok := node[k]; ok {
					var s string
					if json.Unmarshal(r, &s) == nil {
						msg = s
						break
					}
				}
			}
			if msg == "" {
				msg = "上游返回错误事件"
			}
			return msg, true
		}
	}
	return "", false
}