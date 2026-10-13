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
