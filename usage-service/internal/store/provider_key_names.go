package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ProviderKeyName is a display name for an upstream provider credential
// (claude-api-key, codex-api-key, ...). CPA config has no field for it, so the
// panel keeps it here keyed by sha256("<provider>:<api-key>"); the raw key never
// reaches Usage Service.
type ProviderKeyName struct {
	KeyHash     string `json:"keyHash"`
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

var providerKeyNameProviders = map[string]struct{}{
	"gemini": {},
	"codex":  {},
	"claude": {},
	"vertex": {},
	"openai": {},
}

func (s *Store) LoadProviderKeyNames(ctx context.Context) ([]ProviderKeyName, error) {
	rows, err := s.db.QueryContext(ctx, `select key_hash, provider, name, updated_at_ms
		from provider_key_names
		order by provider, name collate nocase, key_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := []ProviderKeyName{}
	for rows.Next() {
		var item ProviderKeyName
		if err := rows.Scan(&item.KeyHash, &item.Provider, &item.Name, &item.UpdatedAtMS); err != nil {
			return nil, err
		}
		names = append(names, item)
	}
	return names, rows.Err()
}

func (s *Store) UpsertProviderKeyNames(ctx context.Context, items []ProviderKeyName) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	normalized := make([]ProviderKeyName, 0, len(items))
	for _, item := range items {
		next, err := normalizeProviderKeyName(item, now)
		if err != nil {
			return err
		}
		normalized = append(normalized, next)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert into provider_key_names (
		key_hash, provider, name, updated_at_ms
	) values (?, ?, ?, ?)
	on conflict(key_hash) do update set
		provider = excluded.provider,
		name = excluded.name,
		updated_at_ms = excluded.updated_at_ms`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range normalized {
		if _, err := stmt.ExecContext(ctx, item.KeyHash, item.Provider, item.Name, item.UpdatedAtMS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteProviderKeyName(ctx context.Context, keyHash string) error {
	hash := strings.ToLower(strings.TrimSpace(keyHash))
	if !validAPIKeyHash(hash) {
		return errors.New("valid keyHash is required")
	}
	_, err := s.db.ExecContext(ctx, `delete from provider_key_names where key_hash = ?`, hash)
	return err
}

func normalizeProviderKeyName(item ProviderKeyName, now int64) (ProviderKeyName, error) {
	hash := strings.ToLower(strings.TrimSpace(item.KeyHash))
	if !validAPIKeyHash(hash) {
		return ProviderKeyName{}, errors.New("valid keyHash is required")
	}
	provider := strings.ToLower(strings.TrimSpace(item.Provider))
	if _, ok := providerKeyNameProviders[provider]; !ok {
		return ProviderKeyName{}, errors.New("unsupported provider")
	}
	name := strings.TrimSpace(item.Name)
	if name == "" {
		return ProviderKeyName{}, errors.New("name is required")
	}
	if len([]rune(name)) > 120 {
		return ProviderKeyName{}, errors.New("name must be 120 characters or less")
	}
	if item.UpdatedAtMS <= 0 {
		item.UpdatedAtMS = now
	}
	item.KeyHash = hash
	item.Provider = provider
	item.Name = name
	return item, nil
}
