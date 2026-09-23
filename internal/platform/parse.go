package platform

import "encoding/json"

func JSONDelta(payload []byte) (string, bool) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(payload, &node); err != nil {
		return "", false
	}

	if raw, ok := node["choices"]; ok {
		var choices []struct {
			Delta *struct {
				Content *string `json:"content"`
				Reason  *string `json:"reasoning_content"`
			} `json:"delta"`
			Messages []struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
			} `json:"messages"`
			FinishReason *string `json:"finish_reason"`
		}
		if err := json.Unmarshal(raw, &choices); err == nil {
			for _, c := range choices {
				if c.FinishReason != nil && *c.FinishReason != "" && len(c.Messages) == 0 && c.Delta == nil {
					return "", true
				}
				if c.Delta != nil && c.Delta.Content != nil {
					return *c.Delta.Content, false
				}
				if c.Delta != nil && c.Delta.Reason != nil {
					return *c.Delta.Reason, false
				}
				for _, m := range c.Messages {
					if m.Content != nil {
						return *m.Content, false
					}
				}
			}
		}
	}

	if raw, ok := node["content"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, false
		}
	}

	if raw, ok := node["text"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, false
		}
	}

	if raw, ok := node["delta"]; ok {
		var delta struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &delta) == nil && delta.Content != "" {
			return delta.Content, false
		}
	}

	if raw, ok := node["finish_reason"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return "", true
		}
	}

	if raw, ok := node["type"]; ok {
		var t string
		if json.Unmarshal(raw, &t) == nil && (t == "done" || t == "error" || t == "finished") {
			return "", true
		}
	}

	return "", false
}

func IsDone(payload []byte) bool {
	s := string(payload)
	return s == "[DONE]" || s == "[done]"
}
