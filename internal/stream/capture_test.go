package stream

import (
	"reflect"
	"testing"
)

func feedAll(d *FrameDecoder, chunks []string, handle func([]byte)) []string {
	var out []string
	for _, c := range chunks {
		d.Feed([]byte(c), func(p []byte) {
			out = append(out, string(p))
		})
	}
	d.Flush(func(p []byte) {
		out = append(out, string(p))
	})
	return out
}

func TestFeedAssembliesFrames(t *testing.T) {
	d := NewFrameDecoder()
	// 人为把两条 SSE 帧切成任意碎片
	data := "data: {\"a\":1}\n\n: keep-alive\n\n" +
		"data: {\"a\":2}\n\n" +
		"data: {\"a\":3}"
	var pieces []string
	for i := 0; i < len(data); i += 7 {
		end := i + 7
		if end > len(data) {
			end = len(data)
		}
		pieces = append(pieces, data[i:end])
	}
	got := feedAll(d, pieces, func(p []byte) {})
	want := []string{`{"a":1}`, `{"a":2}`, `{"a":3}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestCRLFSeparator(t *testing.T) {
	d := NewFrameDecoder()
	got := feedAll(d, []string{"data: x\r\n\r\ndata: y\r\n\r\n"}, func(p []byte) {})
	want := []string{"x", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestCommentsAndEventFiltered(t *testing.T) {
	d := NewFrameDecoder()
	got := feedAll(d, []string{
		": keep-alive\n\n",
		"event: message\ndata: hello\n\n",
		"id: 1\nevent: ping\n\n",
	}, func(p []byte) {})
	if !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMultiLineDataJoined(t *testing.T) {
	d := NewFrameDecoder()
	got := feedAll(d, []string{"data: a\ndata: b\n\n"}, func(p []byte) {})
	if !reflect.DeepEqual(got, []string{"a\nb"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestNoFalseFramesOnPartial(t *testing.T) {
	d := NewFrameDecoder()
	var out []string
	d.Feed([]byte("data: partial"), func(p []byte) { out = append(out, string(p)) })
	if len(out) != 0 {
		t.Fatalf("partial should not emit, got %#v", out)
	}
	d.Feed([]byte(" only\n\n"), func(p []byte) { out = append(out, string(p)) })
	if !reflect.DeepEqual(out, []string{"partial only"}) {
		t.Fatalf("got %#v", out)
	}
}

func TestMatchURL(t *testing.T) {
	if !MatchURL("https://chat.deepseek.com/api/v0/chat/completion", "/api/v0/chat/completion") {
		t.Fatal("should match")
	}
	if MatchURL("https://chat.deepseek.com/static/app.js", "/api/v0/chat/completion") {
		t.Fatal("should not match")
	}
	if !MatchURL("https://x/y", "") {
		t.Fatal("empty keyword matches all")
	}
}