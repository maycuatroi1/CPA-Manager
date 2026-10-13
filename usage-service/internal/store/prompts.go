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

// DuePrompts returns due rows, fresh first attempts first: once the worker
// falls behind, older request logs may already be rotated away.
func (s *Store) DuePrompts(ctx context.Context, nowMS int64, limit int) ([]PendingPrompt, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `select request_id, attempts from request_prompts
		where status = ? and next_attempt_ms <= ?
		order by attempts, next_attempt_ms desc, request_id
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
