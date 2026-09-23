package platform

import "testing"

func TestJSONDeltaStandardChunk(t *testing.T) {
	delta, done := JSONDelta([]byte(`{"choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}`))
	if done || delta != "你好" {
		t.Fatalf("got delta=%q done=%v", delta, done)
	}
}

func TestJSONDeltaReasoningAndContent(t *testing.T) {
	delta, done := JSONDelta([]byte(`{"choices":[{"delta":{"reasoning_content":"思考中","content":"回答"}}]}`))
	if done || delta != "思考中" {
		t.Fatalf("reasoning should be extracted first, got %q", delta)
	}
	delta, _ = JSONDelta([]byte(`{"choices":[{"delta":{"content":"回答"}}]}`))
	if delta != "回答" {
		t.Fatalf("got %q", delta)
	}
}

func TestJSONDeltaContentArray(t *testing.T) {
	delta, _ := JSONDelta([]byte(`{"choices":[{"delta":{"content":["你","好","世界"]}}]}`))
	if delta != "你好世界" {
		t.Fatalf("got %q", delta)
	}
}

func TestJSONDeltaTextDelta(t *testing.T) {
	delta, done := JSONDelta([]byte(`{"type":"text_delta","text":"增量","index":0}`))
	if done || delta != "增量" {
		t.Fatalf("got delta=%q done=%v", delta, done)
	}
}

func TestJSONDeltaMessages(t *testing.T) {
	delta, _ := JSONDelta([]byte(`{"choices":[{"messages":[{"role":"assistant","content":"A"},{"role":"assistant","content":"B"}]}]}`))
	if delta != "A" {
		t.Fatalf("got %q", delta)
	}
}

func TestJSONDeltaFinish(t *testing.T) {
	if _, done := JSONDelta([]byte(`{"type":"finish","text":""}`)); !done {
		t.Fatal("type=finish should mark done")
	}
	if _, done := JSONDelta([]byte(`{"choices":[{"finish_reason":"stop"}]}`)); !done {
		t.Fatal("finish_reason=stop should mark done")
	}
	if _, done := JSONDelta([]byte(`{"is_completion":true}`)); !done {
		t.Fatal("is_completion=true should mark done")
	}
}

func TestJSONDeltaErrorType(t *testing.T) {
	if _, done := JSONDelta([]byte(`{"type":"error","message":"网络错误"}`)); !done {
		t.Fatal("type=error should mark done")
	}
}

func TestIsDone(t *testing.T) {
	if !IsDone([]byte("[DONE]")) || !IsDone([]byte("[done]")) {
		t.Fatal("[DONE] should be detected")
	}
	if IsDone([]byte("[]")) {
		t.Fatal("bogus should not be detected")
	}
}

func TestIsUpstreamError(t *testing.T) {
	msg, ok := IsUpstreamError([]byte(`{"type":"error","message":"请求过于频繁"}`))
	if !ok || msg != "请求过于频繁" {
		t.Fatalf("got msg=%q ok=%v", msg, ok)
	}
	if _, ok := IsUpstreamError([]byte(`{"type":"text_delta","text":"正常"}`)); ok {
		t.Fatal("text_delta is not error")
	}
}

func TestJSONDeltaNoDelta(t *testing.T) {
	delta, done := JSONDelta([]byte(`{"choices":[{"delta":{"content":""},"finish_reason":null}]}`))
	if done || delta != "" {
		t.Fatalf("empty content should yield nothing, got %q/%v", delta, done)
	}
}