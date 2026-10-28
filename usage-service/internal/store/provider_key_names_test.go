package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreProviderKeyNames(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	ctx := context.Background()

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const otherHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := db.UpsertProviderKeyNames(ctx, []ProviderKeyName{
		{KeyHash: hash, Provider: " Claude ", Name: " Acc Canh "},
		{KeyHash: otherHash, Provider: "codex", Name: "Acc Canh"},
	}); err != nil {
		t.Fatalf("upsert names: %v", err)
	}

	names, err := db.LoadProviderKeyNames(ctx)
	if err != nil {
		t.Fatalf("load names: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("len(names) = %d, want 2 (same name across providers is allowed)", len(names))
	}
	byHash := map[string]ProviderKeyName{}
	for _, item := range names {
		byHash[item.KeyHash] = item
	}
	if got := byHash[hash]; got.Provider != "claude" || got.Name != "Acc Canh" || got.UpdatedAtMS <= 0 {
		t.Fatalf("claude name = %#v", got)
	}

	if err := db.UpsertProviderKeyNames(ctx, []ProviderKeyName{
		{KeyHash: hash, Provider: "claude", Name: "Renamed"},
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	names, err = db.LoadProviderKeyNames(ctx)
	if err != nil {
		t.Fatalf("reload names: %v", err)
	}
	for _, item := range names {
		if item.KeyHash == hash && item.Name != "Renamed" {
			t.Fatalf("renamed item = %#v", item)
		}
	}

	invalid := []ProviderKeyName{
		{KeyHash: "short", Provider: "claude", Name: "x"},
		{KeyHash: hash, Provider: "ampcode", Name: "x"},
		{KeyHash: hash, Provider: "claude", Name: "   "},
	}
	for _, item := range invalid {
		if err := db.UpsertProviderKeyNames(ctx, []ProviderKeyName{item}); err == nil {
			t.Fatalf("upsert %#v: want error", item)
		}
	}

	if err := db.DeleteProviderKeyName(ctx, hash); err != nil {
		t.Fatalf("delete name: %v", err)
	}
	names, err = db.LoadProviderKeyNames(ctx)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(names) != 1 || names[0].KeyHash != otherHash {
		t.Fatalf("names after delete = %#v", names)
	}
}
