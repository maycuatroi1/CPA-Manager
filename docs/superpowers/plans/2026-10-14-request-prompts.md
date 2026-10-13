# Request Prompts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Trên trang Monitoring, mỗi call trong bảng realtime có nút "Xem câu hỏi" hiển thị câu người dùng đã gõ, lọc được theo API key và time range.

**Architecture:** usage-service (Go) enqueue `request_id` của mỗi usage event vào bảng SQLite `request_prompts`; một worker nền gọi `GET /v0/management/request-log-by-id/{id}` trên CLIProxyAPI, trích câu hỏi bằng hàm thuần `prompts.ExtractQuestion`, lưu lại; API `GET /v0/management/request-prompts/{id}` trả câu hỏi. Frontend nhận `request_id` trong payload `/usage`, thêm nút + modal ở bảng realtime.

**Tech Stack:** Go 1.25 + SQLite (`database/sql`), React + TypeScript + Vite, vitest (`renderToStaticMarkup`), i18next.

**Spec:** `docs/superpowers/specs/2026-10-14-request-prompts-design.md`

## Global Constraints

- Câu hỏi = text người gõ trong tin `user` mới nhất có text người gõ; bỏ `tool_result`; xoá thẻ `<system-reminder>`, `<command-name>`, `<command-message>`, `<command-args>`, `<local-command-stdout>`, `<local-command-stderr>`, `<local-command-caveat>` (cả nội dung); ảnh → `[image]`; nối bằng `\n\n`; tối đa 8 KB, cắt theo rune, đuôi `…[truncated]`.
- Status: `pending | ok | unavailable | unparsable`; API trả thêm `not_found` khi không có dòng.
- Enqueue với `next_attempt_ms = now + 3s`; worker tick 2s, tối đa 5 item/tick, tuần tự; timeout fetch 30s; đọc body tối đa 20 MB.
- Retry 404/lỗi mạng: backoff 5s, 15s, 45s, 2m, 5m; thất bại lần thứ 6 → `unavailable`. 401/403 không tăng attempts.
- Retention mặc định 30 ngày, env `USAGE_PROMPT_RETENTION_DAYS`, `0` = tắt (không enqueue, không chạy worker). Prune mỗi giờ theo `event_ts_ms`.
- Event từ `/usage/import` không enqueue.
- Không log nội dung câu hỏi ra stdout.
- Modal hiển thị text thuần (`white-space: pre-wrap`), không render HTML/markdown.
- Mọi lệnh Go chạy trong `usage-service/`; mọi lệnh npm chạy ở repo root.

## Review Focus

- Request log quá 20 MB bị cắt → JSON hỏng → phải ra `unparsable`, không panic. (Task 2 có test body JSON cắt cụt.)
- Tin user mới nhất chỉ có `<system-reminder>` (Claude Code chèn sau tool_result) → phải lùi về tin user trước đó. (Task 2 có test.)
- CLIProxyAPI tắt request-log → mọi item 404 mãi → sau 6 lần thành `unavailable`, không vòng lặp vô hạn. (Task 3 có test.)
- Sai management key (401) → worker không đốt attempts của item. (Task 3 có test.)
- Câu hỏi có ký tự nhiều byte (tiếng Việt) dài hơn 8 KB → cắt không vỡ UTF-8. (Task 2 có test.)

---

### Task 1: Bảng `request_prompts` và các hàm store

**Files:**
- Create: `usage-service/internal/store/prompts.go`
- Create: `usage-service/internal/store/prompts_test.go`
- Modify: `usage-service/internal/store/store.go` (thêm câu lệnh vào `statements` trong `init()`)

**Interfaces:**
- Produces:
  - `const PromptStatusPending = "pending"`, `PromptStatusOK = "ok"`, `PromptStatusUnavailable = "unavailable"`, `PromptStatusUnparsable = "unparsable"`
  - `type PromptEnqueue struct { RequestID string; EventTSMS int64 }`
  - `type PendingPrompt struct { RequestID string; Attempts int }`
  - `type RequestPrompt struct { RequestID string \`json:"request_id"\`; Status string \`json:"status"\`; Question string \`json:"question"\` }`
  - `func (s *Store) EnqueuePrompts(ctx context.Context, items []PromptEnqueue, nextAttemptMS int64, nowMS int64) error`
  - `func (s *Store) DuePrompts(ctx context.Context, nowMS int64, limit int) ([]PendingPrompt, error)`
  - `func (s *Store) SavePromptResult(ctx context.Context, requestID string, status string, question string, nowMS int64) error`
  - `func (s *Store) RetryPrompt(ctx context.Context, requestID string, attempts int, nextAttemptMS int64, nowMS int64) error`
  - `func (s *Store) GetPrompt(ctx context.Context, requestID string) (RequestPrompt, bool, error)`
  - `func (s *Store) DeletePromptsBefore(ctx context.Context, cutoffMS int64) (int64, error)`

- [ ] **Step 1: Write the failing test**

`usage-service/internal/store/prompts_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openPromptTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestEnqueuePromptsIsIdempotentAndSkipsEmptyIDs(t *testing.T) {
	db := openPromptTestStore(t)
	ctx := context.Background()

	items := []PromptEnqueue{{RequestID: "req-1", EventTSMS: 100}, {RequestID: "  ", EventTSMS: 100}}
	if err := db.EnqueuePrompts(ctx, items, 1_000, 500); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.SavePromptResult(ctx, "req-1", PromptStatusOK, "hello", 600); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Re-enqueueing an existing id must not reset it to pending.
	if err := db.EnqueuePrompts(ctx, items, 1_000, 700); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	prompt, ok, err := db.GetPrompt(ctx, "req-1")
	if err != nil || !ok {
		t.Fatalf("get prompt ok=%v err=%v", ok, err)
	}
	if prompt.Status != PromptStatusOK || prompt.Question != "hello" {
		t.Fatalf("prompt = %#v", prompt)
	}
	if _, ok, _ := db.GetPrompt(ctx, ""); ok {
		t.Fatalf("empty request id must not be stored")
	}
}

func TestDuePromptsHonoursNextAttemptAndRetry(t *testing.T) {
	db := openPromptTestStore(t)
	ctx := context.Background()

	if err := db.EnqueuePrompts(ctx, []PromptEnqueue{{RequestID: "a", EventTSMS: 1}, {RequestID: "b", EventTSMS: 1}}, 1_000, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if due, _ := db.DuePrompts(ctx, 999, 10); len(due) != 0 {
		t.Fatalf("due before next_attempt = %#v", due)
	}
	due, err := db.DuePrompts(ctx, 1_000, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("due limit 1 = %#v err=%v", due, err)
	}

	if err := db.RetryPrompt(ctx, "a", 2, 5_000, 1_000); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := db.SavePromptResult(ctx, "b", PromptStatusUnavailable, "", 1_000); err != nil {
		t.Fatalf("save: %v", err)
	}
	if due, _ := db.DuePrompts(ctx, 4_999, 10); len(due) != 0 {
		t.Fatalf("due before retry time = %#v", due)
	}
	due, _ = db.DuePrompts(ctx, 5_000, 10)
	if len(due) != 1 || due[0].RequestID != "a" || due[0].Attempts != 2 {
		t.Fatalf("due after retry time = %#v", due)
	}
}

func TestDeletePromptsBeforeUsesEventTimestamp(t *testing.T) {
	db := openPromptTestStore(t)
	ctx := context.Background()

	items := []PromptEnqueue{{RequestID: "old", EventTSMS: 100}, {RequestID: "new", EventTSMS: 900}}
	if err := db.EnqueuePrompts(ctx, items, 0, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deleted, err := db.DeletePromptsBefore(ctx, 500)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if _, ok, _ := db.GetPrompt(ctx, "old"); ok {
		t.Fatalf("old prompt should be deleted")
	}
	if _, ok, _ := db.GetPrompt(ctx, "new"); !ok {
		t.Fatalf("new prompt should remain")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run Prompt`
Expected: FAIL to build — `undefined: PromptEnqueue`.

- [ ] **Step 3: Add the table to `init()`**

In `usage-service/internal/store/store.go`, append to the `statements` slice right after the `api_key_limits` create statement:

```go
		`create table if not exists request_prompts (
			request_id text primary key,
			status text not null,
			question text,
			attempts integer not null default 0,
			next_attempt_ms integer not null,
			event_ts_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_request_prompts_pending on request_prompts(status, next_attempt_ms)`,
		`create index if not exists idx_request_prompts_event_ts on request_prompts(event_ts_ms)`,
```

- [ ] **Step 4: Write the store functions**

`usage-service/internal/store/prompts.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const (
	PromptStatusPending     = "pending"
	PromptStatusOK          = "ok"
	PromptStatusUnavailable = "unavailable"
	PromptStatusUnparsable  = "unparsable"
)

type PromptEnqueue struct {
	RequestID string
	EventTSMS int64
}

type PendingPrompt struct {
	RequestID string
	Attempts  int
}

type RequestPrompt struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Question  string `json:"question"`
}

// EnqueuePrompts registers request ids whose question should be fetched.
// Existing rows are left untouched so a re-delivered event cannot reset them.
func (s *Store) EnqueuePrompts(ctx context.Context, items []PromptEnqueue, nextAttemptMS int64, nowMS int64) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	stmt, err := tx.PrepareContext(ctx, `insert or ignore into request_prompts (
		request_id, status, attempts, next_attempt_ms, event_ts_ms, updated_at_ms
	) values (?, ?, 0, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		requestID := strings.TrimSpace(item.RequestID)
		if requestID == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, requestID, PromptStatusPending, nextAttemptMS, item.EventTSMS, nowMS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DuePrompts(ctx context.Context, nowMS int64, limit int) ([]PendingPrompt, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `select request_id, attempts from request_prompts
		where status = ? and next_attempt_ms <= ?
		order by next_attempt_ms, request_id
		limit ?`, PromptStatusPending, nowMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PendingPrompt, 0)
	for rows.Next() {
		var item PendingPrompt
		if err := rows.Scan(&item.RequestID, &item.Attempts); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SavePromptResult(ctx context.Context, requestID string, status string, question string, nowMS int64) error {
	_, err := s.db.ExecContext(ctx, `update request_prompts
		set status = ?, question = ?, updated_at_ms = ?
		where request_id = ?`, status, nullString(question), nowMS, requestID)
	return err
}

func (s *Store) RetryPrompt(ctx context.Context, requestID string, attempts int, nextAttemptMS int64, nowMS int64) error {
	_, err := s.db.ExecContext(ctx, `update request_prompts
		set attempts = ?, next_attempt_ms = ?, updated_at_ms = ?
		where request_id = ?`, attempts, nextAttemptMS, nowMS, requestID)
	return err
}

func (s *Store) GetPrompt(ctx context.Context, requestID string) (RequestPrompt, bool, error) {
	var prompt RequestPrompt
	var question sql.NullString
	err := s.db.QueryRowContext(ctx, `select request_id, status, question
		from request_prompts where request_id = ?`, strings.TrimSpace(requestID)).
		Scan(&prompt.RequestID, &prompt.Status, &question)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestPrompt{}, false, nil
	}
	if err != nil {
		return RequestPrompt{}, false, err
	}
	prompt.Question = question.String
	return prompt, true, nil
}

func (s *Store) DeletePromptsBefore(ctx context.Context, cutoffMS int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `delete from request_prompts where event_ts_ms < ?`, cutoffMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
```

(`nullString` already exists in `store.go` and maps `""` to SQL NULL.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add usage-service/internal/store/prompts.go usage-service/internal/store/prompts_test.go usage-service/internal/store/store.go
git commit -m "✨ feat(usage-service): add request_prompts store"
```

---

### Task 2: `prompts.ExtractQuestion`

**Files:**
- Create: `usage-service/internal/prompts/extract.go`
- Create: `usage-service/internal/prompts/extract_test.go`

**Interfaces:**
- Consumes: `store.PromptStatusOK`, `store.PromptStatusUnparsable` (Task 1).
- Produces: `func ExtractQuestion(logFile []byte) (question string, status string)`; `const MaxQuestionBytes = 8 * 1024`.

- [ ] **Step 1: Write the failing test**

`usage-service/internal/prompts/extract_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompts`
Expected: FAIL to build — `undefined: ExtractQuestion`.

- [ ] **Step 3: Write the implementation**

`usage-service/internal/prompts/extract.go`:

```go
package prompts

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

// MaxQuestionBytes caps how much of a question is stored per request.
const MaxQuestionBytes = 8 * 1024

const (
	truncatedMarker  = "…[truncated]"
	imagePlaceholder = "[image]"
	bodySectionTitle = "=== REQUEST BODY ==="
)

var sectionHeaderPattern = regexp.MustCompile(`(?m)^=== [A-Z][A-Z ]* ===\s*$`)

// autoTagPatterns match blocks that clients such as Claude Code inject into
// user turns; they are not text the person typed.
var autoTagPatterns = func() []*regexp.Regexp {
	tags := []string{
		"system-reminder",
		"command-name",
		"command-message",
		"command-args",
		"local-command-stdout",
		"local-command-stderr",
		"local-command-caveat",
	}
	patterns := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		patterns = append(patterns, regexp.MustCompile(`(?s)<`+tag+`>.*?</`+tag+`>`))
	}
	return patterns
}()

type turnPiece struct {
	text  string
	image bool
}

// ExtractQuestion returns the latest text the user typed in the request body
// of a CLIProxyAPI request log file.
func ExtractQuestion(logFile []byte) (string, string) {
	body := requestBody(logFile)
	if len(body) == 0 {
		return "", store.PromptStatusUnparsable
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", store.PromptStatusUnparsable
	}
	turns := userTurns(payload)
	for i := len(turns) - 1; i >= 0; i-- {
		if question, ok := renderTurn(turns[i]); ok {
			return truncateQuestion(question), store.PromptStatusOK
		}
	}
	return "", store.PromptStatusUnparsable
}

func requestBody(logFile []byte) []byte {
	start := bytes.Index(logFile, []byte(bodySectionTitle))
	if start < 0 {
		return nil
	}
	rest := logFile[start+len(bodySectionTitle):]
	if next := sectionHeaderPattern.FindIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	return bytes.TrimSpace(rest)
}

func userTurns(payload map[string]any) [][]turnPiece {
	if messages, ok := payload["messages"].([]any); ok {
		return turnsFromItems(messages, "content")
	}
	if contents, ok := payload["contents"].([]any); ok {
		return turnsFromItems(contents, "parts")
	}
	switch input := payload["input"].(type) {
	case string:
		return [][]turnPiece{{{text: input}}}
	case []any:
		return turnsFromItems(input, "content")
	}
	return nil
}

func turnsFromItems(items []any, contentKey string) [][]turnPiece {
	turns := make([][]turnPiece, 0)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := item["role"].(string); role != "user" {
			continue
		}
		turns = append(turns, piecesFromContent(item[contentKey]))
	}
	return turns
}

func piecesFromContent(content any) []turnPiece {
	switch value := content.(type) {
	case string:
		return []turnPiece{{text: value}}
	case []any:
		pieces := make([]turnPiece, 0, len(value))
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "tool_result", "function_call_output", "tool_use":
				continue
			case "image", "image_url", "input_image":
				pieces = append(pieces, turnPiece{image: true})
				continue
			}
			if block["inline_data"] != nil || block["inlineData"] != nil {
				pieces = append(pieces, turnPiece{image: true})
				continue
			}
			if text, ok := block["text"].(string); ok && (blockType == "" || blockType == "text" || blockType == "input_text") {
				pieces = append(pieces, turnPiece{text: text})
			}
		}
		return pieces
	}
	return nil
}

// renderTurn joins a turn's pieces; ok is false when the turn has no typed text.
func renderTurn(pieces []turnPiece) (string, bool) {
	parts := make([]string, 0, len(pieces))
	hasText := false
	for _, piece := range pieces {
		if piece.image {
			parts = append(parts, imagePlaceholder)
			continue
		}
		cleaned := piece.text
		for _, pattern := range autoTagPatterns {
			cleaned = pattern.ReplaceAllString(cleaned, "")
		}
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		parts = append(parts, cleaned)
		hasText = true
	}
	return strings.Join(parts, "\n\n"), hasText
}

func truncateQuestion(question string) string {
	if len(question) <= MaxQuestionBytes {
		return question
	}
	cut := question[:MaxQuestionBytes-len(truncatedMarker)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + truncatedMarker
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/prompts`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add usage-service/internal/prompts/extract.go usage-service/internal/prompts/extract_test.go
git commit -m "✨ feat(usage-service): extract typed question from request logs"
```

---

### Task 3: Prompt worker

**Files:**
- Create: `usage-service/internal/prompts/worker.go`
- Create: `usage-service/internal/prompts/worker_test.go`

**Interfaces:**
- Consumes: `store.Store` prompt methods (Task 1), `ExtractQuestion` (Task 2).
- Produces:
  - `var ErrUnauthorized error`
  - `func NewWorker(st *store.Store, interval time.Duration) *Worker`
  - `func (w *Worker) RunOnce(ctx context.Context, baseURL string, managementKey string) error`
  - `func (w *Worker) Prune(ctx context.Context, retention time.Duration) error`
  - `func (w *Worker) Run(ctx context.Context, baseURL string, managementKey string, retention time.Duration)`

- [ ] **Step 1: Write the failing test**

`usage-service/internal/prompts/worker_test.go`:

```go
package prompts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

const testLog = "=== REQUEST BODY ===\n{\"messages\":[{\"role\":\"user\",\"content\":\"hỏi gì đó\"}]}\n\n=== RESPONSE ===\nStatus: 200\n"

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newWorkerTest(t *testing.T, handler http.HandlerFunc) (*Worker, *store.Store, *fakeClock, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	clock := &fakeClock{now: time.UnixMilli(1_000_000)}
	worker := NewWorker(db, time.Second)
	worker.now = clock.Now
	if err := db.EnqueuePrompts(context.Background(), []store.PromptEnqueue{{RequestID: "abc123", EventTSMS: 1}}, 0, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return worker, db, clock, upstream.URL
}

func TestWorkerStoresQuestionOnSuccess(t *testing.T) {
	worker, db, _, base := newWorkerTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/request-log-by-id/abc123" || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(testLog))
	})
	if err := worker.RunOnce(context.Background(), base, "key"); err != nil {
		t.Fatalf("run once: %v", err)
	}
	prompt, ok, _ := db.GetPrompt(context.Background(), "abc123")
	if !ok || prompt.Status != store.PromptStatusOK || prompt.Question != "hỏi gì đó" {
		t.Fatalf("prompt = %#v", prompt)
	}
}

func TestWorkerRetriesNotFoundThenSucceeds(t *testing.T) {
	var calls int32
	worker, db, clock, base := newWorkerTest(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(testLog))
	})
	ctx := context.Background()
	if err := worker.RunOnce(ctx, base, "key"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	due, _ := db.DuePrompts(ctx, clock.now.Add(5*time.Second).UnixMilli(), 10)
	if len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("after 404 due = %#v", due)
	}
	if err := worker.RunOnce(ctx, base, "key"); err != nil {
		t.Fatalf("not yet due run: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("fetched before backoff elapsed")
	}
	clock.now = clock.now.Add(5 * time.Second)
	if err := worker.RunOnce(ctx, base, "key"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if prompt, _, _ := db.GetPrompt(ctx, "abc123"); prompt.Status != store.PromptStatusOK {
		t.Fatalf("status = %q", prompt.Status)
	}
}

func TestWorkerGivesUpAfterSixFailures(t *testing.T) {
	worker, db, clock, base := newWorkerTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if err := worker.RunOnce(ctx, base, "key"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		clock.now = clock.now.Add(10 * time.Minute)
	}
	if prompt, _, _ := db.GetPrompt(ctx, "abc123"); prompt.Status != store.PromptStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", prompt.Status)
	}
}

func TestWorkerUnauthorizedKeepsAttempts(t *testing.T) {
	worker, db, clock, base := newWorkerTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	})
	ctx := context.Background()
	if err := worker.RunOnce(ctx, base, "key"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	due, _ := db.DuePrompts(ctx, clock.now.UnixMilli(), 10)
	if len(due) != 1 || due[0].Attempts != 0 {
		t.Fatalf("due = %#v", due)
	}
}

func TestWorkerPruneDeletesOldPrompts(t *testing.T) {
	worker, db, clock, _ := newWorkerTest(t, func(w http.ResponseWriter, r *http.Request) {})
	clock.now = time.UnixMilli(1).Add(48 * time.Hour)
	if err := worker.Prune(context.Background(), 24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, ok, _ := db.GetPrompt(context.Background(), "abc123"); ok {
		t.Fatalf("prompt older than retention should be deleted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prompts -run Worker`
Expected: FAIL to build — `undefined: NewWorker`.

- [ ] **Step 3: Write the implementation**

`usage-service/internal/prompts/worker.go`:

```go
package prompts

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

const (
	workerBatchSize = 5
	maxLogBytes     = 20 << 20
	pruneEvery      = time.Hour
)

// retryDelays is the backoff after each failed fetch; one more failure than
// its length marks the prompt unavailable.
var retryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute}

var ErrUnauthorized = errors.New("request log fetch unauthorized")

// Worker fetches CLIProxyAPI request logs for pending request ids and stores
// the user's question.
type Worker struct {
	store    *store.Store
	client   *http.Client
	interval time.Duration
	now      func() time.Time
}

func NewWorker(st *store.Store, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{
		store:    st,
		client:   &http.Client{Timeout: 30 * time.Second},
		interval: interval,
		now:      time.Now,
	}
}

func (w *Worker) Run(ctx context.Context, baseURL string, managementKey string, retention time.Duration) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(managementKey) == "" || retention <= 0 {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	var lastPrune time.Time
	for {
		if w.now().Sub(lastPrune) >= pruneEvery {
			if err := w.Prune(ctx, retention); err != nil && ctx.Err() == nil {
				log.Printf("prompts: prune failed: %v", err)
			}
			lastPrune = w.now()
		}
		if err := w.RunOnce(ctx, baseURL, managementKey); err != nil && ctx.Err() == nil {
			log.Printf("prompts: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) Prune(ctx context.Context, retention time.Duration) error {
	_, err := w.store.DeletePromptsBefore(ctx, w.now().Add(-retention).UnixMilli())
	return err
}

func (w *Worker) RunOnce(ctx context.Context, baseURL string, managementKey string) error {
	due, err := w.store.DuePrompts(ctx, w.now().UnixMilli(), workerBatchSize)
	if err != nil {
		return err
	}
	for _, item := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.process(ctx, baseURL, managementKey, item); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) process(ctx context.Context, baseURL string, managementKey string, item store.PendingPrompt) error {
	body, status, fetchErr := w.fetch(ctx, baseURL, managementKey, item.RequestID)
	nowMS := w.now().UnixMilli()
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrUnauthorized
	case fetchErr == nil && status == http.StatusOK:
		question, result := ExtractQuestion(body)
		return w.store.SavePromptResult(ctx, item.RequestID, result, question, nowMS)
	default:
		attempts := item.Attempts + 1
		if attempts > len(retryDelays) {
			return w.store.SavePromptResult(ctx, item.RequestID, store.PromptStatusUnavailable, "", nowMS)
		}
		return w.store.RetryPrompt(ctx, item.RequestID, attempts, nowMS+retryDelays[attempts-1].Milliseconds(), nowMS)
	}
}

func (w *Worker) fetch(ctx context.Context, baseURL string, managementKey string, requestID string) ([]byte, int, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") +
		"/v0/management/request-log-by-id/" + url.PathEscape(requestID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(managementKey))
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
	return body, resp.StatusCode, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/prompts`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add usage-service/internal/prompts/worker.go usage-service/internal/prompts/worker_test.go
git commit -m "✨ feat(usage-service): add request prompt fetch worker"
```

---

### Task 4: Config + collector enqueue + start worker

**Files:**
- Modify: `usage-service/internal/config/config.go` (field `PromptRetentionDays`, file field, env loader, helper)
- Modify: `usage-service/internal/config/config_test.go`
- Modify: `usage-service/internal/collector/collector.go` (`run`, `processItems`, new `enqueuePrompts`, package vars)
- Modify: `usage-service/internal/collector/collector_test.go`

**Interfaces:**
- Consumes: `store.EnqueuePrompts`, `store.PromptEnqueue`, `store.GetPrompt` (Task 1); `prompts.NewWorker`, `(*Worker).Run` (Task 3).
- Produces: `config.Config.PromptRetentionDays int`.

- [ ] **Step 1: Write the failing config test**

Append to `usage-service/internal/config/config_test.go` (it already imports `path/filepath` and `testing`):

```go
func TestLoadPromptRetentionDays(t *testing.T) {
	t.Setenv(configEnvKey, filepath.Join(t.TempDir(), "config.json"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PromptRetentionDays != 30 {
		t.Fatalf("default PromptRetentionDays = %d, want 30", cfg.PromptRetentionDays)
	}

	t.Setenv("USAGE_PROMPT_RETENTION_DAYS", "0")
	if cfg, _ = Load(); cfg.PromptRetentionDays != 0 {
		t.Fatalf("PromptRetentionDays with 0 = %d, want 0", cfg.PromptRetentionDays)
	}

	t.Setenv("USAGE_PROMPT_RETENTION_DAYS", "7")
	if cfg, _ = Load(); cfg.PromptRetentionDays != 7 {
		t.Fatalf("PromptRetentionDays with 7 = %d, want 7", cfg.PromptRetentionDays)
	}
}
```

- [ ] **Step 2: Write the failing collector test**

Append to `usage-service/internal/collector/collector_test.go`:

```go
func TestManagerFetchesPromptForCollectedEvent(t *testing.T) {
	previousDelay, previousInterval := promptInitialDelay, promptWorkerInterval
	promptInitialDelay, promptWorkerInterval = 0, 20*time.Millisecond
	t.Cleanup(func() { promptInitialDelay, promptWorkerInterval = previousDelay, previousInterval })

	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/usage-queue":
			w.Header().Set("Content-Type", "application/json")
			if atomic.AddInt32(&calls, 1) == 1 {
				_, _ = w.Write([]byte(`[{"timestamp":"2026-05-06T00:00:00Z","request_id":"req-42","model":"m","endpoint":"POST /v1/messages","input_tokens":1,"output_tokens":1}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/v0/management/request-log-by-id/req-42":
			_, _ = w.Write([]byte("=== REQUEST BODY ===\n{\"messages\":[{\"role\":\"user\",\"content\":\"ai hỏi\"}]}\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	cfg := testConfig(t, "http")
	cfg.PromptRetentionDays = 30
	manager := NewManager(cfg, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})

	waitFor(t, func() bool {
		prompt, ok, err := db.GetPrompt(context.Background(), "req-42")
		return err == nil && ok && prompt.Status == store.PromptStatusOK && prompt.Question == "ai hỏi"
	})
}

func TestManagerSkipsPromptsWhenRetentionDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/usage-queue" {
			_, _ = w.Write([]byte(`[{"timestamp":"2026-05-06T00:00:00Z","request_id":"req-off","model":"m","input_tokens":1}]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	manager := NewManager(testConfig(t, "http"), db) // PromptRetentionDays = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})

	waitFor(t, func() bool {
		events, _, err := db.Counts(context.Background())
		return err == nil && events >= 1
	})
	if _, ok, _ := db.GetPrompt(context.Background(), "req-off"); ok {
		t.Fatalf("prompt must not be enqueued when retention is 0")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/config ./internal/collector`
Expected: FAIL to build — `cfg.PromptRetentionDays undefined` and `undefined: promptInitialDelay`.

- [ ] **Step 4: Implement config**

In `usage-service/internal/config/config.go`:

1. Add to `Config` struct after `TLSSkipVerify bool`:

```go
	PromptRetentionDays int
```

2. Add to `fileConfig` struct:

```go
	PromptRetentionDays int `json:"promptRetentionDays,omitempty"`
```

3. In the `return Config{...}` literal inside `Load`, after `TLSSkipVerify: ...`:

```go
		PromptRetentionDays: envNonNegativeInt("USAGE_PROMPT_RETENTION_DAYS", intFallback(cfgFile.PromptRetentionDays, 30)),
```

4. Add below `envInt`:

```go
// envNonNegativeInt is envInt but keeps an explicit 0, which disables a feature.
func envNonNegativeInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}
```

Run `gofmt -w internal/config/config.go` to realign the struct literal.

- [ ] **Step 5: Implement collector wiring**

In `usage-service/internal/collector/collector.go`:

1. Add imports `"log"` and `"github.com/seakee/cpa-manager/usage-service/internal/prompts"`.

2. Add below the imports:

```go
// Overridable in tests.
var (
	promptInitialDelay   = 3 * time.Second
	promptWorkerInterval = 2 * time.Second
)
```

3. At the top of `func (m *Manager) run(ctx context.Context, cfg RuntimeConfig)`, before `mode := ...`:

```go
	if retention := m.promptRetention(); retention > 0 {
		go prompts.NewWorker(m.store, promptWorkerInterval).Run(ctx, cfg.CPAUpstreamURL, cfg.ManagementKey, retention)
	}
```

4. In `processItems`, right after the `InsertEvents` error check (`if err != nil { return err }`):

```go
	m.enqueuePrompts(ctx, events)
```

5. Add methods:

```go
func (m *Manager) promptRetention() time.Duration {
	return time.Duration(m.base.PromptRetentionDays) * 24 * time.Hour
}

func (m *Manager) enqueuePrompts(ctx context.Context, events []usage.Event) {
	if m.promptRetention() <= 0 || len(events) == 0 {
		return
	}
	items := make([]store.PromptEnqueue, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.RequestID) == "" {
			continue
		}
		items = append(items, store.PromptEnqueue{RequestID: event.RequestID, EventTSMS: event.TimestampMS})
	}
	now := time.Now().UnixMilli()
	if err := m.store.EnqueuePrompts(ctx, items, now+promptInitialDelay.Milliseconds(), now); err != nil {
		log.Printf("collector: enqueue prompts failed: %v", err)
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/config ./internal/collector`
Expected: `ok` for both.

- [ ] **Step 7: Commit**

```bash
git add usage-service/internal/config usage-service/internal/collector
git commit -m "✨ feat(usage-service): enqueue request prompts from collector"
```

---

### Task 5: HTTP API — `request_id` in usage payload + `/request-prompts/{id}`

**Files:**
- Modify: `usage-service/internal/usage/event.go` (`Detail`, `BuildPayload`)
- Modify: `usage-service/internal/httpapi/server.go` (`handleRoot`, new `handleRequestPrompt`)
- Modify: `usage-service/internal/httpapi/server_test.go`

**Interfaces:**
- Consumes: `store.GetPrompt`, `store.EnqueuePrompts`, `store.SavePromptResult` (Task 1).
- Produces: JSON field `request_id` on each usage detail; `GET /v0/management/request-prompts/{id}` → `{"request_id","status","question"}`, 404 → `{"request_id","status":"not_found"}`.

- [ ] **Step 1: Write the failing test**

In `usage-service/internal/httpapi/server_test.go`, replace the body of `newTestHandler` so tests can reach the store:

```go
func newTestHandler(t *testing.T, upstreamURL string, saveSetup bool) http.Handler {
	t.Helper()
	handler, _ := newTestHandlerAndStore(t, upstreamURL, saveSetup)
	return handler
}

func newTestHandlerAndStore(t *testing.T, upstreamURL string, saveSetup bool) (http.Handler, *store.Store) {
	t.Helper()

	cfg := config.Config{
		DBPath:      filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:       "usage",
		PopSide:     "right",
		CORSOrigins: []string{"*"},
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if saveSetup {
		err := db.SaveSetup(context.Background(), store.Setup{
			CPAUpstreamURL: upstreamURL,
			ManagementKey:  "management-key",
			Queue:          "usage",
			PopSide:        "right",
		})
		if err != nil {
			t.Fatalf("save setup: %v", err)
		}
	}

	manager := collector.NewManager(cfg, db)
	return New(cfg, db, manager).Handler(), db
}
```

Append the tests:

```go
func TestRequestPromptEndpoint(t *testing.T) {
	handler, db := newTestHandlerAndStore(t, "http://127.0.0.1:1", true)
	ctx := context.Background()
	if err := db.EnqueuePrompts(ctx, []store.PromptEnqueue{{RequestID: "abc123", EventTSMS: 1}}, 0, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.SavePromptResult(ctx, "abc123", store.PromptStatusOK, "câu hỏi", 1); err != nil {
		t.Fatalf("save: %v", err)
	}

	get := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer management-key")
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	if rr := get("/v0/management/request-prompts/abc123", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d", rr.Code)
	}

	rr := get("/v0/management/request-prompts/abc123", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var found store.RequestPrompt
	if err := json.Unmarshal(rr.Body.Bytes(), &found); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if found.Status != store.PromptStatusOK || found.Question != "câu hỏi" || found.RequestID != "abc123" {
		t.Fatalf("prompt = %#v", found)
	}

	rr = get("/v0/management/request-prompts/missing", true)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"not_found"`) {
		t.Fatalf("missing status = %d body = %s", rr.Code, rr.Body.String())
	}

	if rr := get("/v0/management/request-prompts/", true); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty id status = %d", rr.Code)
	}
}

func TestUsagePayloadIncludesRequestID(t *testing.T) {
	handler := newTestHandler(t, "http://127.0.0.1:1", true)
	postUsageImport(t, handler, `{"timestamp":1778000001000,"model":"m","endpoint":"POST /v1/messages","request_id":"rid-7"}`)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	req.Header.Set("Authorization", "Bearer management-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"request_id":"rid-7"`) {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi -run "RequestPrompt|UsagePayloadIncludesRequestID"`
Expected: FAIL — `/request-prompts/abc123` is proxied upstream (status 502, not 200) and `request_id` missing from usage body.

- [ ] **Step 3: Add `request_id` to usage details**

In `usage-service/internal/usage/event.go`, add as the first field of `Detail`:

```go
	RequestID            string `json:"request_id,omitempty"`
```

and in `BuildPayload`, inside `modelEntry.Details = append(modelEntry.Details, Detail{`, add:

```go
			RequestID:            event.RequestID,
```

- [ ] **Step 4: Add the route and handler**

In `usage-service/internal/httpapi/server.go`, inside `handleRoot`, insert before the `cleanUsagePath := ...` line:

```go
	if strings.HasPrefix(r.URL.Path, "/v0/management/request-prompts/") {
		s.withCORS(s.handleRequestPrompt)(w, r)
		return
	}
```

Add below `handleUsageImport`:

```go
func (s *Server) handleRequestPrompt(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeIfConfigured(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	requestID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v0/management/request-prompts/"))
	if requestID == "" || strings.Contains(requestID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("request id is required"))
		return
	}
	prompt, ok, err := s.store.GetPrompt(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"request_id": requestID, "status": "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}
```

- [ ] **Step 5: Run the whole Go suite**

Run: `go vet ./... && go test ./...`
Expected: all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add usage-service/internal/usage/event.go usage-service/internal/httpapi
git commit -m "✨ feat(usage-service): expose request prompts and usage request ids"
```

---

### Task 6: Frontend data layer

**Files:**
- Modify: `src/utils/usage.ts` (`UsageDetail`, `collectUsageDetailsWithEndpoint`)
- Create: `src/utils/usageRequestId.test.ts`
- Modify: `src/features/monitoring/hooks/useMonitoringData.ts` (`MonitoringEventRow.requestId`, `buildEventRows`)
- Modify: `src/features/monitoring/hooks/useMonitoringData.test.ts` and `src/features/monitoring/accountOverviewState.test.ts` (row builders)
- Modify: `src/services/api/usageService.ts` (types + `getRequestPrompt`)
- Modify: `src/features/monitoring/hooks/useUsageData.ts` (`getRequestPrompt`)

**Interfaces:**
- Consumes: API from Task 5.
- Produces:
  - `UsageDetail.request_id?: string`
  - `MonitoringEventRow.requestId: string` (`''` when absent)
  - `export type RequestPromptStatus = 'pending' | 'ok' | 'unavailable' | 'unparsable' | 'not_found'`
  - `export interface RequestPromptResponse { request_id: string; status: RequestPromptStatus; question?: string }`
  - `usageServiceApi.getRequestPrompt(base: string, managementKey: string | undefined, requestId: string): Promise<RequestPromptResponse>`
  - `UseUsageDataReturn.getRequestPrompt: (requestId: string) => Promise<RequestPromptResponse>`

- [ ] **Step 1: Write the failing test**

`src/utils/usageRequestId.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { collectUsageDetailsWithEndpoint } from './usage';

describe('collectUsageDetailsWithEndpoint request_id', () => {
  it('keeps the request id of each detail', () => {
    const details = collectUsageDetailsWithEndpoint({
      apis: {
        'POST /v1/messages': {
          models: {
            'claude-test': {
              details: [
                { timestamp: '2026-10-14T01:00:00Z', source: 's', request_id: 'abc123', tokens: {}, failed: false },
                { timestamp: '2026-10-14T01:01:00Z', source: 's', tokens: {}, failed: false },
              ],
            },
          },
        },
      },
    });

    expect(details.map((detail) => detail.request_id)).toEqual(['abc123', undefined]);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/utils/usageRequestId.test.ts`
Expected: FAIL — received `[undefined, undefined]`.

- [ ] **Step 3: Implement**

In `src/utils/usage.ts`:
- add `request_id?: string;` to `interface UsageDetail` (after `timestamp: string;`);
- in `collectUsageDetailsWithEndpoint`, inside `details.push({`, after `timestamp,` add:

```ts
          request_id: readDetailString(detailRaw.request_id ?? detailRaw.requestId),
```

In `src/features/monitoring/hooks/useMonitoringData.ts`:
- add `requestId: string;` to `MonitoringEventRow` after `id: string;`;
- in `buildEventRows`, in the returned object after `id: \`...\`,` add:

```ts
        requestId: readString(detail.request_id),
```

In both test row builders (`useMonitoringData.test.ts` `createMonitoringEventRow`, `accountOverviewState.test.ts` `createEventRow`), add after the `searchText:` line:

```ts
  requestId: overrides.requestId ?? '',
```

In `src/services/api/usageService.ts`, add the types near the other exported interfaces:

```ts
export type RequestPromptStatus = 'pending' | 'ok' | 'unavailable' | 'unparsable' | 'not_found';

export interface RequestPromptResponse {
  request_id: string;
  status: RequestPromptStatus;
  question?: string;
}
```

and the method inside `usageServiceApi` after `getUsage`:

```ts
  getRequestPrompt: async (
    base: string,
    managementKey: string | undefined,
    requestId: string
  ): Promise<RequestPromptResponse> => {
    return withUsageServiceError(async () => {
      try {
        const response = await axios.get<RequestPromptResponse>(
          buildUrl(base, `/v0/management/request-prompts/${encodeURIComponent(requestId)}`),
          { timeout: USAGE_SERVICE_TIMEOUT_MS, headers: authHeaders(managementKey) }
        );
        return response.data;
      } catch (err) {
        if (axios.isAxiosError(err) && err.response?.status === 404) {
          return { request_id: requestId, status: 'not_found' };
        }
        throw err;
      }
    });
  },
```

In `src/features/monitoring/hooks/useUsageData.ts`:
- import `type RequestPromptResponse` from `@/services/api/usageService` (add to the existing import list);
- add to `UseUsageDataReturn`: `getRequestPrompt: (requestId: string) => Promise<RequestPromptResponse>;`
- add after `exportUsageFromApi`:

```ts
  const getRequestPrompt = useCallback(
    async (requestId: string): Promise<RequestPromptResponse> => {
      const serviceBase = await resolveUsageServiceBase();
      if (!serviceBase) {
        throw new Error('usage_service_unavailable');
      }
      return usageServiceApi.getRequestPrompt(serviceBase, managementKey, requestId);
    },
    [managementKey, resolveUsageServiceBase]
  );
```

- add `getRequestPrompt,` to the returned object.

- [ ] **Step 4: Verify**

Run: `npx vitest run src/utils/usageRequestId.test.ts && npm run type-check`
Expected: test PASS, type-check exits 0.

- [ ] **Step 5: Commit**

```bash
git add src/utils/usage.ts src/utils/usageRequestId.test.ts src/features/monitoring src/services/api/usageService.ts
git commit -m "✨ feat(monitoring): carry request ids and fetch request prompts"
```

---

### Task 7: Frontend UI — nút "Xem câu hỏi" + modal

**Files:**
- Create: `src/features/monitoring/components/RequestPromptView.tsx`
- Create: `src/features/monitoring/components/RequestPromptView.test.tsx`
- Modify: `src/pages/MonitoringCenterPage.tsx` (state, column, modal)
- Modify: `src/pages/MonitoringCenterPage.module.scss` (one class)
- Modify: `src/i18n/locales/en.json`, `zh-CN.json`, `zh-TW.json`, `ru.json` (`monitoring.*` keys)

**Interfaces:**
- Consumes: `RequestPromptResponse` (Task 6), `getRequestPrompt` from `useUsageData` (Task 6), `MonitoringEventRow.requestId` (Task 6).
- Produces:
  - `export type RequestPromptViewState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; response: RequestPromptResponse }`
  - `export function RequestPromptView(props: { t: TFunction; state: RequestPromptViewState; meta: { time: string; apiKey: string; model: string; tokens: string }; onRetry: () => void; onCopy: (text: string) => void })`

- [ ] **Step 1: Write the failing test**

`src/features/monitoring/components/RequestPromptView.test.tsx`:

```tsx
import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import { RequestPromptView, type RequestPromptViewState } from './RequestPromptView';

const t = ((key: string) => key) as unknown as TFunction;
const meta = { time: '14/10/2026 01:00', apiKey: 'Cảnh', model: 'claude-opus', tokens: '1.2K' };

const render = (state: RequestPromptViewState) =>
  renderToStaticMarkup(
    <RequestPromptView t={t} state={state} meta={meta} onRetry={() => {}} onCopy={() => {}} />
  );

describe('RequestPromptView', () => {
  it('shows the question as plain text with meta', () => {
    const markup = render({
      kind: 'loaded',
      response: { request_id: 'abc', status: 'ok', question: '<b>ai hỏi</b>' },
    });
    expect(markup).toContain('&lt;b&gt;ai hỏi&lt;/b&gt;');
    expect(markup).toContain('Cảnh');
    expect(markup).toContain('claude-opus');
    expect(markup).toContain('monitoring.request_prompt_copy');
  });

  it.each([
    ['pending', 'monitoring.request_prompt_pending'],
    ['unavailable', 'monitoring.request_prompt_unavailable'],
    ['unparsable', 'monitoring.request_prompt_unparsable'],
    ['not_found', 'monitoring.request_prompt_not_found'],
  ] as const)('renders %s status message', (status, key) => {
    const markup = render({ kind: 'loaded', response: { request_id: 'abc', status } });
    expect(markup).toContain(key);
    expect(markup).not.toContain('monitoring.request_prompt_copy');
  });

  it('renders loading and error states', () => {
    expect(render({ kind: 'loading' })).toContain('monitoring.request_prompt_loading');
    const error = render({ kind: 'error', message: 'boom' });
    expect(error).toContain('boom');
    expect(error).toContain('monitoring.request_prompt_retry');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/features/monitoring/components/RequestPromptView.test.tsx`
Expected: FAIL — cannot resolve `./RequestPromptView`.

- [ ] **Step 3: Write the component**

`src/features/monitoring/components/RequestPromptView.tsx`:

```tsx
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import type { RequestPromptResponse } from '@/services/api/usageService';
import styles from '@/pages/MonitoringCenterPage.module.scss';

export type RequestPromptViewState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; response: RequestPromptResponse };

type RequestPromptViewProps = {
  t: TFunction;
  state: RequestPromptViewState;
  meta: { time: string; apiKey: string; model: string; tokens: string };
  onRetry: () => void;
  onCopy: (text: string) => void;
};

const STATUS_MESSAGE_KEYS: Record<string, string> = {
  pending: 'monitoring.request_prompt_pending',
  unavailable: 'monitoring.request_prompt_unavailable',
  unparsable: 'monitoring.request_prompt_unparsable',
  not_found: 'monitoring.request_prompt_not_found',
};

export function RequestPromptView({ t, state, meta, onRetry, onCopy }: RequestPromptViewProps) {
  const question =
    state.kind === 'loaded' && state.response.status === 'ok' ? state.response.question || '' : '';

  let body;
  if (state.kind === 'loading') {
    body = <p>{t('monitoring.request_prompt_loading')}</p>;
  } else if (state.kind === 'error') {
    body = (
      <div>
        <p>{state.message}</p>
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {t('monitoring.request_prompt_retry')}
        </Button>
      </div>
    );
  } else if (question) {
    body = (
      <div>
        <pre className={styles.requestPromptText}>{question}</pre>
        <Button variant="secondary" size="sm" onClick={() => onCopy(question)}>
          {t('monitoring.request_prompt_copy')}
        </Button>
      </div>
    );
  } else {
    const messageKey =
      STATUS_MESSAGE_KEYS[state.response.status] ?? 'monitoring.request_prompt_unparsable';
    body = (
      <div>
        <p>{t(messageKey)}</p>
        {state.response.status === 'pending' ? (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            {t('monitoring.request_prompt_retry')}
          </Button>
        ) : null}
      </div>
    );
  }

  return (
    <div>
      <div className={styles.primaryCell}>
        <span>{`${meta.apiKey} · ${meta.model}`}</span>
        <small>{`${meta.time} · ${meta.tokens} tokens`}</small>
      </div>
      {body}
    </div>
  );
}
```

Add to `src/pages/MonitoringCenterPage.module.scss` (end of file):

```scss
.requestPromptText {
  margin: 12px 0;
  max-height: 60vh;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: inherit;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/features/monitoring/components/RequestPromptView.test.tsx`
Expected: PASS.

- [ ] **Step 5: Wire into the page**

In `src/pages/MonitoringCenterPage.tsx`:

1. Imports:

```tsx
import {
  RequestPromptView,
  type RequestPromptViewState,
} from '@/features/monitoring/components/RequestPromptView';
import { copyToClipboard } from '@/utils/clipboard';
```

2. Add `getRequestPrompt,` to the destructuring of `useUsageData({ timeRange, customTimeRange })`.

3. After the `refreshAll` callback, add:

```tsx
  const [promptRow, setPromptRow] = useState<MonitoringEventRow | null>(null);
  const [promptState, setPromptState] = useState<RequestPromptViewState>({ kind: 'loading' });
  const promptSeqRef = useRef(0);

  const loadRequestPrompt = useCallback(
    async (row: MonitoringEventRow) => {
      const seq = promptSeqRef.current + 1;
      promptSeqRef.current = seq;
      setPromptState({ kind: 'loading' });
      try {
        const response = await getRequestPrompt(row.requestId);
        if (promptSeqRef.current === seq) setPromptState({ kind: 'loaded', response });
      } catch (err) {
        if (promptSeqRef.current === seq) {
          setPromptState({
            kind: 'error',
            message: err instanceof Error ? err.message : String(err),
          });
        }
      }
    },
    [getRequestPrompt]
  );

  const openRequestPrompt = useCallback(
    (row: MonitoringEventRow) => {
      setPromptRow(row);
      void loadRequestPrompt(row);
    },
    [loadRequestPrompt]
  );

  const copyRequestPrompt = useCallback(
    async (text: string) => {
      const copied = await copyToClipboard(text);
      showNotification(
        copied ? t('monitoring.request_prompt_copied') : t('notification.copy_failed'),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, t]
  );
```

(`useRef`, `useCallback`, `useState` and `type MonitoringEventRow` are already imported in this file; `notification.copy_failed` already exists.)

4. In the realtime table header, after `<th>{t('monitoring.this_call_cost')}</th>`:

```tsx
                <th>{t('monitoring.request_prompt_column')}</th>
```

5. In the row, after the cost cell `<td>{hasPrices ? formatUsd(row.totalCost) : '--'}</td>`:

```tsx
                    <td>
                      {row.requestId ? (
                        <Button variant="secondary" size="sm" onClick={() => openRequestPrompt(row)}>
                          {t('monitoring.request_prompt_view')}
                        </Button>
                      ) : (
                        '--'
                      )}
                    </td>
```

6. Change the empty-state `<td colSpan={10}>` in the same table to `<td colSpan={11}>`.

7. Next to the page's other `<Modal>` elements (or just before the component's final closing tag), add:

```tsx
      <Modal
        open={Boolean(promptRow)}
        onClose={() => setPromptRow(null)}
        title={t('monitoring.request_prompt_title')}
        width={720}
      >
        {promptRow ? (
          <RequestPromptView
            t={t}
            state={promptState}
            meta={{
              time: new Date(promptRow.timestampMs).toLocaleString(i18n.language),
              apiKey: promptRow.apiKeyLabel,
              model: promptRow.model,
              tokens: formatCompactNumber(promptRow.totalTokens),
            }}
            onRetry={() => void loadRequestPrompt(promptRow)}
            onCopy={(text) => void copyRequestPrompt(text)}
          />
        ) : null}
      </Modal>
```

- [ ] **Step 6: Add i18n keys**

Add to the `"monitoring"` object of each locale file, next to `"this_call_cost"`.

`en.json`:

```json
    "request_prompt_column": "Question",
    "request_prompt_view": "View question",
    "request_prompt_title": "Question",
    "request_prompt_loading": "Loading question…",
    "request_prompt_pending": "The question is still being fetched. Try again in a minute.",
    "request_prompt_unavailable": "The request log for this call is no longer available.",
    "request_prompt_unparsable": "Could not read a question from this request.",
    "request_prompt_not_found": "No question recorded for this call (made before the feature was enabled, or the feature is off).",
    "request_prompt_retry": "Retry",
    "request_prompt_copy": "Copy",
    "request_prompt_copied": "Question copied to clipboard",
```

`zh-CN.json`:

```json
    "request_prompt_column": "问题",
    "request_prompt_view": "查看问题",
    "request_prompt_title": "问题",
    "request_prompt_loading": "正在加载问题…",
    "request_prompt_pending": "问题仍在获取中，请稍后重试。",
    "request_prompt_unavailable": "该调用的请求日志已不可用。",
    "request_prompt_unparsable": "无法从该请求中读取问题。",
    "request_prompt_not_found": "该调用没有记录问题（功能启用前的调用，或功能已关闭）。",
    "request_prompt_retry": "重试",
    "request_prompt_copy": "复制",
    "request_prompt_copied": "问题已复制到剪贴板",
```

`zh-TW.json`:

```json
    "request_prompt_column": "問題",
    "request_prompt_view": "查看問題",
    "request_prompt_title": "問題",
    "request_prompt_loading": "正在載入問題…",
    "request_prompt_pending": "問題仍在取得中，請稍後重試。",
    "request_prompt_unavailable": "該呼叫的請求記錄已不可用。",
    "request_prompt_unparsable": "無法從該請求中讀取問題。",
    "request_prompt_not_found": "該呼叫沒有記錄問題（功能啟用前的呼叫，或功能已關閉）。",
    "request_prompt_retry": "重試",
    "request_prompt_copy": "複製",
    "request_prompt_copied": "問題已複製到剪貼簿",
```

`ru.json` (English strings, as the repo does for untranslated text):

```json
    "request_prompt_column": "Question",
    "request_prompt_view": "View question",
    "request_prompt_title": "Question",
    "request_prompt_loading": "Loading question…",
    "request_prompt_pending": "The question is still being fetched. Try again in a minute.",
    "request_prompt_unavailable": "The request log for this call is no longer available.",
    "request_prompt_unparsable": "Could not read a question from this request.",
    "request_prompt_not_found": "No question recorded for this call (made before the feature was enabled, or the feature is off).",
    "request_prompt_retry": "Retry",
    "request_prompt_copy": "Copy",
    "request_prompt_copied": "Question copied to clipboard",
```

- [ ] **Step 7: Verify everything**

Run: `npm run type-check && npx eslint src/features/monitoring src/pages/MonitoringCenterPage.tsx src/services/api/usageService.ts src/utils/usage.ts && npm test`
Expected: type-check and eslint clean; all vitest files PASS.

Run (in `usage-service/`): `go vet ./... && go test ./...`
Expected: all `ok`.

- [ ] **Step 8: Commit**

```bash
git add src/features/monitoring src/pages/MonitoringCenterPage.tsx src/pages/MonitoringCenterPage.module.scss src/i18n/locales
git commit -m "✨ feat(monitoring): view request question from realtime table"
```

---

### Task 8: Push và mở PR

- [ ] **Step 1:** `git push -u origin feat/request-prompts`
- [ ] **Step 2:** Mở PR vào `main` bằng `gh pr create`, mô tả: mục tiêu, điều kiện CLIProxyAPI (`request-log: true`, `commercial-mode: false`), env `USAGE_PROMPT_RETENTION_DAYS`, kết quả test; kết thúc bằng dòng `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
