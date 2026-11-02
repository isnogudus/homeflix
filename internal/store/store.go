// Package store owns the SQLite database: opening it, schema migrations and
// queries.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// FileName is the database file inside the data directory.
const FileName = "homeflix.db"

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open creates dataDir if needed, opens the database in it and applies all
// pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, FileName)
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: bad version prefix", e.Name())
		}
		body, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: versions must be contiguous from 1", m.name)
		}
	}
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, len(ms))
	}
	for _, m := range ms[current:] {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Setting returns the value stored under key and whether it exists.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting stores value under key, replacing any previous value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// ServerID returns the persistent server ID, creating it on first use. It is
// a 32-digit lowercase hex string like Jellyfin's.
func (s *Store) ServerID(ctx context.Context) (string, error) {
	id, ok, err := s.Setting(ctx, "server_id")
	if err != nil || ok {
		return id, err
	}
	id, err = NewID()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES('server_id', ?) ON CONFLICT(key) DO NOTHING", id)
	if err != nil {
		return "", err
	}
	id, _, err = s.Setting(ctx, "server_id")
	return id, err
}

// NewID returns a random 128-bit ID as 32 lowercase hex digits.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
