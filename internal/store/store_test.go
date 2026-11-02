package store

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
)

func openTemp(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := openTemp(t, filepath.Join(t.TempDir(), "with space", "data"))
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != len(ms) {
		t.Errorf("schema version %d, want %d", v, len(ms))
	}
}

func TestServerIDIsStable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s := openTemp(t, dir)
	first, err := s.ServerID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(first) {
		t.Errorf("server ID %q is not 32 hex digits", first)
	}
	s.Close()

	second, err := openTemp(t, dir).ServerID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("server ID changed across reopen: %q != %q", first, second)
	}
}

func TestSettingRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	if _, ok, err := s.Setting(ctx, "missing"); err != nil || ok {
		t.Fatalf("Setting(missing) = ok %v, err %v", ok, err)
	}
	if err := s.SetSetting(ctx, "k", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "b"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Setting(ctx, "k")
	if err != nil || !ok || v != "b" {
		t.Errorf("Setting(k) = %q, %v, %v", v, ok, err)
	}
}
