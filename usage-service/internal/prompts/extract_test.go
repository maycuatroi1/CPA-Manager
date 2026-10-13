package prompts

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

func logWithBody(body string) []byte {
	return []byte("=== REQUEST INFO ===\nURL: /v1/messages\n\n=== HEADERS ===\nContent-Type: application/json\n\n=== REQUEST BODY ===\n" +
		body + "\n\n=== RESPONSE ===\nStatus: 200\n{\"ok\":true}\n")
}

func TestExtractQuestion(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   string
		status string
	}{
		{
			name:   "anthropic string content",
			body:   `{"messages":[{"role":"user","content":"xin chào"}]}`,
			want:   "xin chào",
			status: store.PromptStatusOK,
		},
		{
			name: "anthropic blocks drop system reminders",
			body: `{"messages":[{"role":"user","content":[
				{"type":"text","text":"<system-reminder>auto context</system-reminder>"},
				{"type":"text","text":"ok viết spec đi"},
				{"type":"image","source":{"type":"base64","data":"AAA"}}
			]}]}`,
			want:   "ok viết spec đi\n\n[image]",
			status: store.PromptStatusOK,
		},
		{
			name: "skips trailing tool_result and reminder-only turns",
			body: `{"messages":[
				{"role":"user","content":[{"type":"text","text":"câu hỏi thật"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"output"},{"type":"text","text":"<system-reminder>x</system-reminder>"}]}
			]}`,
			want:   "câu hỏi thật",
			status: store.PromptStatusOK,
		},
		{
			name:   "strips claude code command tags",
			body:   `{"messages":[{"role":"user","content":"<command-name>/foo</command-name><command-args>a</command-args>làm đi"}]}`,
			want:   "làm đi",
			status: store.PromptStatusOK,
		},
		{
			name:   "openai chat completions",
			body:   `{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"x"}}]}]}`,
			want:   "hello\n\n[image]",
			status: store.PromptStatusOK,
		},
		{
			name:   "openai responses string input",
			body:   `{"model":"gpt","input":"what is go"}`,
			want:   "what is go",
			status: store.PromptStatusOK,
		},
		{
			name:   "openai responses item input",
			body:   `{"input":[{"role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"function_call_output","call_id":"c","output":"x"}]}`,
			want:   "first",
			status: store.PromptStatusOK,
		},
		{
			name:   "gemini contents",
			body:   `{"contents":[{"role":"user","parts":[{"text":"gemini q"},{"inline_data":{"mime_type":"image/png","data":"AA"}}]}]}`,
			want:   "gemini q\n\n[image]",
			status: store.PromptStatusOK,
		},
		{
			name:   "no human text",
			body:   `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`,
			status: store.PromptStatusUnparsable,
		},
		{
			name:   "truncated json",
			body:   `{"messages":[{"role":"user","content":"cut`,
			status: store.PromptStatusUnparsable,
		},
		{
			name:   "unknown shape",
			body:   `{"prompt":"legacy"}`,
			status: store.PromptStatusUnparsable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, status := ExtractQuestion(logWithBody(tc.body))
			if status != tc.status || got != tc.want {
				t.Fatalf("got (%q, %q), want (%q, %q)", got, status, tc.want, tc.status)
			}
		})
	}
}

func TestExtractQuestionWithoutBodySection(t *testing.T) {
	if _, status := ExtractQuestion([]byte("=== REQUEST INFO ===\nURL: /x\n")); status != store.PromptStatusUnparsable {
		t.Fatalf("status = %q", status)
	}
}

func TestExtractQuestionTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("ữ", MaxQuestionBytes) // 3 bytes each
	got, status := ExtractQuestion(logWithBody(`{"messages":[{"role":"user","content":"` + long + `"}]}`))
	if status != store.PromptStatusOK {
		t.Fatalf("status = %q", status)
	}
	if len(got) > MaxQuestionBytes {
		t.Fatalf("len = %d, want <= %d", len(got), MaxQuestionBytes)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…[truncated]") {
		t.Fatalf("bad truncation suffix/utf8: %q", got[len(got)-20:])
	}
}
