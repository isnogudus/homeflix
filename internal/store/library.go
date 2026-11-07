package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Library is a configured media library.
type Library struct {
	ID             string
	Name           string
	CollectionType string
}

// Item is a movie or an extra of a movie.
type Item struct {
	ID           string
	LibraryID    string
	ParentID     string // set for extras
	Type         string // "Movie" or "Video"
	ExtraType    string // for extras, e.g. "BehindTheScenes"
	Path         string // movie folder or single file
	Title        string
	SortTitle    string
	Year         int // 0 if unknown
	IMDbID       string
	TMDbID       string
	DateCreated  time.Time
	MissingSince time.Time // zero while present
	Files        []MediaFile
}

// MediaFile is one file of one version of an item.
type MediaFile struct {
	ID         string
	ItemID     string
	Version    string // label; "" when the item has a single version
	Part       int    // 1, 2, ... for split versions; 0 otherwise
	Path       string
	Size       int64
	ModTime    time.Time
	Probe      string // ffprobe JSON; "" until probed
	ProbeError string
	ProbedAt   time.Time
	Duration   time.Duration
	Container  string
	BitRate    int64
	// AudioMeta is the JSON of media.AudioMeta for audio files, "" for
	// others and until probed. Files of tracks are loaded with it in place
	// of the probe data.
	AudioMeta string
}

// EnsureLibrary returns the library with this name, creating it if needed;
// a changed collection type is stored.
func (s *Store) EnsureLibrary(ctx context.Context, name, collectionType string) (Library, error) {
	var lib Library
	err := s.db.QueryRowContext(ctx, "SELECT id, name, collection_type FROM libraries WHERE name = ?", name).
		Scan(&lib.ID, &lib.Name, &lib.CollectionType)
	if err == nil {
		if lib.CollectionType != collectionType {
			if _, err := s.db.ExecContext(ctx, "UPDATE libraries SET collection_type = ? WHERE id = ?",
				collectionType, lib.ID); err != nil {
				return Library{}, err
			}
			lib.CollectionType = collectionType
		}
		return lib, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Library{}, err
	}
	id, err := NewID()
	if err != nil {
		return Library{}, err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO libraries(id, name, collection_type) VALUES(?, ?, ?)",
		id, name, collectionType); err != nil {
		return Library{}, err
	}
	return Library{ID: id, Name: name, CollectionType: collectionType}, nil
}

// Libraries lists all libraries ordered by name.
func (s *Store) Libraries(ctx context.Context) ([]Library, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, collection_type FROM libraries ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var libs []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.CollectionType); err != nil {
			return nil, err
		}
		libs = append(libs, l)
	}
	return libs, rows.Err()
}

const itemColumns = "id, library_id, parent_id, type, extra_type, path, title, sort_title, year, imdb_id, tmdb_id, date_created, missing_since"

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var parent sql.NullString
	var year, created, missing sql.NullInt64
	err := row.Scan(&it.ID, &it.LibraryID, &parent, &it.Type, &it.ExtraType, &it.Path, &it.Title, &it.SortTitle,
		&year, &it.IMDbID, &it.TMDbID, &created, &missing)
	if err != nil {
		return Item{}, err
	}
	it.ParentID = parent.String
	it.Year = int(year.Int64)
	it.DateCreated, it.MissingSince = fromMillis(created), fromMillis(missing)
	return it, nil
}

const fileColumns = "id, item_id, version, part, path, size, mtime, probe, probe_error, probed_at, duration_ms, container, bit_rate, audio_meta"

func scanFile(row interface{ Scan(...any) error }) (MediaFile, error) {
	var f MediaFile
	var probe, probeErr, audio sql.NullString
	var mtime, probedAt, duration, bitRate sql.NullInt64
	err := row.Scan(&f.ID, &f.ItemID, &f.Version, &f.Part, &f.Path, &f.Size, &mtime, &probe, &probeErr,
		&probedAt, &duration, &f.Container, &bitRate, &audio)
	if err != nil {
		return MediaFile{}, err
	}
	f.Probe, f.ProbeError, f.AudioMeta = probe.String, probeErr.String, audio.String
	f.ModTime, f.ProbedAt = fromMillis(mtime), fromMillis(probedAt)
	f.Duration = time.Duration(duration.Int64) * time.Millisecond
	f.BitRate = bitRate.Int64
	return f, nil
}

// LibraryItems returns every item of a library, including extras and
// missing items, with their files but without probe data.
func (s *Store) LibraryItems(ctx context.Context, libraryID string) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+itemColumns+" FROM items WHERE library_id = ?", libraryID)
	if err != nil {
		return nil, err
	}
	var items []Item
	index := map[string]int{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[it.ID] = len(items)
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	frows, err := s.db.QueryContext(ctx, `SELECT f.id, f.item_id, f.version, f.part, f.path, f.size, f.mtime,
		NULL, f.probe_error, f.probed_at, f.duration_ms, f.container, f.bit_rate, NULL
		FROM media_files f JOIN items i ON i.id = f.item_id WHERE i.library_id = ?
		ORDER BY f.version, f.part`, libraryID)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		f, err := scanFile(frows)
		if err != nil {
			return nil, err
		}
		if i, ok := index[f.ItemID]; ok {
			items[i].Files = append(items[i].Files, f)
		}
	}
	return items, frows.Err()
}

// PresentItems returns all items that are not missing, of every library,
// with their files including probe data; files of tracks (items of type
// Audio) come with their audio metadata instead.
func (s *Store) PresentItems(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+itemColumns+" FROM items WHERE missing_since IS NULL")
	if err != nil {
		return nil, err
	}
	var items []Item
	index := map[string]int{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[it.ID] = len(items)
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	frows, err := s.db.QueryContext(ctx, `SELECT f.id, f.item_id, f.version, f.part, f.path, f.size, f.mtime,
		CASE WHEN i.type = 'Audio' THEN NULL ELSE f.probe END, f.probe_error, f.probed_at, f.duration_ms,
		f.container, f.bit_rate, f.audio_meta
		FROM media_files f JOIN items i ON i.id = f.item_id WHERE i.missing_since IS NULL
		ORDER BY f.version, f.part`)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		f, err := scanFile(frows)
		if err != nil {
			return nil, err
		}
		if i, ok := index[f.ItemID]; ok {
			items[i].Files = append(items[i].Files, f)
		}
	}
	return items, frows.Err()
}

// FileUpdate changes the location or content of a known file.
type FileUpdate struct {
	File       MediaFile
	ResetProbe bool // the content changed; probe it again
}

// ScanChanges is the result of comparing a library scan with the database.
type ScanChanges struct {
	NewItems       []Item // parents before their extras; Files are inserted too
	UpdatedItems   []Item // Files are ignored
	DeletedItemIDs []string
	NewFiles       []MediaFile
	UpdatedFiles   []FileUpdate
	DeletedFileIDs []string
}

// Empty reports whether there is nothing to apply.
func (c ScanChanges) Empty() bool {
	return len(c.NewItems)+len(c.UpdatedItems)+len(c.DeletedItemIDs)+
		len(c.NewFiles)+len(c.UpdatedFiles)+len(c.DeletedFileIDs) == 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// ApplyScan writes scan changes in one transaction. Paths are unique, so
// updated rows first get a placeholder path; that way renames that swap
// or reuse paths within one scan cannot collide.
func (s *Store) ApplyScan(ctx context.Context, ch ScanChanges) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}
	for _, id := range ch.DeletedFileIDs {
		if err := exec("DELETE FROM media_files WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
	}
	for _, id := range ch.DeletedItemIDs {
		if err := exec("DELETE FROM items WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete item: %w", err)
		}
	}
	for _, it := range ch.UpdatedItems {
		if err := exec("UPDATE items SET path = ? WHERE id = ?", "\x00moving:"+it.ID, it.ID); err != nil {
			return err
		}
	}
	for _, u := range ch.UpdatedFiles {
		if err := exec("UPDATE media_files SET path = ? WHERE id = ?", "\x00moving:"+u.File.ID, u.File.ID); err != nil {
			return err
		}
	}
	insertFile := func(f MediaFile) error {
		return exec("INSERT INTO media_files(id, item_id, version, part, path, size, mtime) VALUES(?, ?, ?, ?, ?, ?, ?)",
			f.ID, f.ItemID, f.Version, f.Part, f.Path, f.Size, f.ModTime.UnixMilli())
	}
	for _, it := range ch.NewItems {
		err := exec("INSERT INTO items("+itemColumns+") VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			it.ID, it.LibraryID, nullString(it.ParentID), it.Type, it.ExtraType, it.Path, it.Title, it.SortTitle,
			nullInt(it.Year), it.IMDbID, it.TMDbID, it.DateCreated.UnixMilli(), toMillis(it.MissingSince))
		if err != nil {
			return fmt.Errorf("insert item %s: %w", it.Path, err)
		}
		for _, f := range it.Files {
			if err := insertFile(f); err != nil {
				return fmt.Errorf("insert file %s: %w", f.Path, err)
			}
		}
	}
	for _, it := range ch.UpdatedItems {
		err := exec(`UPDATE items SET parent_id = ?, type = ?, extra_type = ?, path = ?, title = ?, sort_title = ?,
			year = ?, imdb_id = ?, tmdb_id = ?, missing_since = ? WHERE id = ?`,
			nullString(it.ParentID), it.Type, it.ExtraType, it.Path, it.Title, it.SortTitle, nullInt(it.Year),
			it.IMDbID, it.TMDbID, toMillis(it.MissingSince), it.ID)
		if err != nil {
			return fmt.Errorf("update item %s: %w", it.Path, err)
		}
	}
	for _, f := range ch.NewFiles {
		if err := insertFile(f); err != nil {
			return fmt.Errorf("insert file %s: %w", f.Path, err)
		}
	}
	for _, u := range ch.UpdatedFiles {
		f := u.File
		q := "UPDATE media_files SET item_id = ?, version = ?, part = ?, path = ?, size = ?, mtime = ?"
		if u.ResetProbe {
			q += ", probe = NULL, probe_error = NULL, probed_at = NULL, duration_ms = NULL, container = '', bit_rate = NULL, audio_meta = NULL"
		}
		if err := exec(q+" WHERE id = ?", f.ItemID, f.Version, f.Part, f.Path, f.Size, f.ModTime.UnixMilli(), f.ID); err != nil {
			return fmt.Errorf("update file %s: %w", f.Path, err)
		}
	}
	return tx.Commit()
}

// UnprobedFiles returns files of present items that have neither probe data
// nor a probe error.
func (s *Store) UnprobedFiles(ctx context.Context) ([]MediaFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.item_id, f.version, f.part, f.path, f.size, f.mtime,
		NULL, NULL, NULL, NULL, f.container, NULL, NULL
		FROM media_files f JOIN items i ON i.id = f.item_id
		WHERE f.probe IS NULL AND f.probe_error IS NULL AND i.missing_since IS NULL
		ORDER BY f.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []MediaFile
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ProbeResult is what probing one file produced.
type ProbeResult struct {
	JSON      string
	Duration  time.Duration
	Container string
	BitRate   int64
	Err       string // set instead of JSON when probing failed
	// AudioMeta is the JSON of media.AudioMeta for audio files.
	AudioMeta string
}

// SetProbeResult stores a probe result, unless the file changed since it
// was read for probing.
func (s *Store) SetProbeResult(ctx context.Context, f MediaFile, r ProbeResult, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media_files SET probe = ?, probe_error = ?, probed_at = ?,
		duration_ms = ?, container = ?, bit_rate = ?, audio_meta = ? WHERE id = ? AND size = ? AND mtime = ?`,
		nullString(r.JSON), nullString(r.Err), at.UnixMilli(), nullInt(int(r.Duration.Milliseconds())),
		r.Container, nullInt(int(r.BitRate)), nullString(r.AudioMeta), f.ID, f.Size, f.ModTime.UnixMilli())
	return err
}

// ResetProbes discards all probe results and errors so every file is
// probed again.
func (s *Store) ResetProbes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media_files SET probe = NULL, probe_error = NULL, probed_at = NULL,
		duration_ms = NULL, container = '', bit_rate = NULL, audio_meta = NULL`)
	return err
}

// PurgeMissing deletes items that have been missing since before the given
// time, together with their extras and files.
func (s *Store) PurgeMissing(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM items WHERE missing_since IS NOT NULL AND missing_since < ?",
		before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ProbedFile is a file with its probe data, for reports.
type ProbedFile struct {
	MediaFile
	ItemType string
	Title    string
	Year     int
}

// AllFiles calls fn for every file of present items, probe data included.
func (s *Store) AllFiles(ctx context.Context, fn func(ProbedFile) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.item_id, f.version, f.part, f.path, f.size, f.mtime,
		f.probe, f.probe_error, f.probed_at, f.duration_ms, f.container, f.bit_rate, i.type, i.title, i.year
		FROM media_files f JOIN items i ON i.id = f.item_id WHERE i.missing_since IS NULL ORDER BY f.path`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pf ProbedFile
		var probe, probeErr sql.NullString
		var mtime, probedAt, duration, bitRate, year sql.NullInt64
		if err := rows.Scan(&pf.ID, &pf.ItemID, &pf.Version, &pf.Part, &pf.Path, &pf.Size, &mtime, &probe, &probeErr,
			&probedAt, &duration, &pf.Container, &bitRate, &pf.ItemType, &pf.Title, &year); err != nil {
			return err
		}
		pf.Probe, pf.ProbeError = probe.String, probeErr.String
		pf.ModTime, pf.ProbedAt = fromMillis(mtime), fromMillis(probedAt)
		pf.Duration = time.Duration(duration.Int64) * time.Millisecond
		pf.BitRate, pf.Year = bitRate.Int64, int(year.Int64)
		if err := fn(pf); err != nil {
			return err
		}
	}
	return rows.Err()
}

// FileProbe returns the stored probe data of one file, "" if there is none.
func (s *Store) FileProbe(ctx context.Context, fileID string) (string, error) {
	var probe sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT probe FROM media_files WHERE id = ?", fileID).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return probe.String, err
}
