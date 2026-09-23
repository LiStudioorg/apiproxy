package router

import (
	"testing"

	"web2api/api"
)

func TestBuildPromptMultiTurn(t *testing.T) {
	p := buildPrompt([]api.Message{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好"},
		{Role: "user", Content: "今天天气"},
	})
	want := "[system]\n你是助手\n\n[user]\n你好\n\n[assistant]\n你好\n\n[user]\n今天天气"
	if p != want {
		t.Fatalf("got:\n%s\nwant:\n%s", p, want)
	}
}

func TestBuildPromptSkipsBlank(t *testing.T) {
	p := buildPrompt([]api.Message{{Role: "user", Content: "  "}})
	if p != "" {
		t.Fatalf("blank should be skipped, got %q", p)
	}
}

func TestBuildPromptEmpty(t *testing.T) {
	if p := buildPrompt(nil); p != "" {
		t.Fatalf("got %q", p)
	}
}