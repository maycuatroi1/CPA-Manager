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
