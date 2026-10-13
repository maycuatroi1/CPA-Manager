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
	maxErrorBackoff = 5 * time.Minute
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

	// OnError, when set, is called with the worker's current error whenever
	// it changes (nil once fetching works again).
	OnError   func(error)
	lastError string
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
	var lastPrune time.Time
	delay := w.interval
	for {
		if w.now().Sub(lastPrune) >= pruneEvery {
			if err := w.Prune(ctx, retention); err != nil && ctx.Err() == nil {
				log.Printf("prompts: prune failed: %v", err)
			}
			lastPrune = w.now()
		}
		err := w.RunOnce(ctx, baseURL, managementKey)
		if ctx.Err() != nil {
			return
		}
		w.report(err)
		delay = w.nextDelay(err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// nextDelay backs off exponentially while upstream keeps failing so a bad key
// or an outage is not retried every tick.
func (w *Worker) nextDelay(err error, previous time.Duration) time.Duration {
	if err == nil {
		return w.interval
	}
	next := previous * 2
	if next < w.interval*2 {
		next = w.interval * 2
	}
	if next > maxErrorBackoff {
		next = maxErrorBackoff
	}
	return next
}

// report logs and forwards the error only when it changes.
func (w *Worker) report(err error) {
	current := ""
	if err != nil {
		current = err.Error()
	}
	if current == w.lastError {
		return
	}
	w.lastError = current
	if err != nil {
		log.Printf("prompts: %v", err)
	} else {
		log.Printf("prompts: fetching recovered")
	}
	if w.OnError != nil {
		w.OnError(err)
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
