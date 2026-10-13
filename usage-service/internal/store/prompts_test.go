package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

// Every pooled connection must wait on locks: the prompt worker and the
// collector write concurrently, and a SQLITE_BUSY would drop a usage batch.
func TestEveryConnectionHasBusyTimeout(t *testing.T) {
	db := openPromptTestStore(t)
	ctx := context.Background()

	first, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatalf("first conn: %v", err)
	}
	defer first.Close()
	second, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatalf("second conn: %v", err)
	}
	defer second.Close()

	for name, conn := range map[string]*sql.Conn{"first": first, "second": second} {
		var timeout int
		if err := conn.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&timeout); err != nil {
			t.Fatalf("%s busy_timeout: %v", name, err)
		}
		if timeout < 5000 {
			t.Fatalf("%s connection busy_timeout = %d, want >= 5000", name, timeout)
		}
	}
}

// Fresh first attempts go before retries and older rows: once the worker
// falls behind, old logs may already be rotated away.
func TestDuePromptsPrefersFreshFirstAttempts(t *testing.T) {
	db := openPromptTestStore(t)
	ctx := context.Background()
	if err := db.EnqueuePrompts(ctx, []PromptEnqueue{{RequestID: "old", EventTSMS: 1}}, 100, 0); err != nil {
		t.Fatalf("enqueue old: %v", err)
	}
	if err := db.EnqueuePrompts(ctx, []PromptEnqueue{{RequestID: "retry", EventTSMS: 1}}, 50, 0); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	if err := db.RetryPrompt(ctx, "retry", 1, 150, 0); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := db.EnqueuePrompts(ctx, []PromptEnqueue{{RequestID: "new", EventTSMS: 1}}, 200, 0); err != nil {
		t.Fatalf("enqueue new: %v", err)
	}
	due, err := db.DuePrompts(ctx, 1_000, 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	var order []string
	for _, item := range due {
		order = append(order, item.RequestID)
	}
	if strings.Join(order, ",") != "new,old,retry" {
		t.Fatalf("order = %v, want [new old retry]", order)
	}
}
