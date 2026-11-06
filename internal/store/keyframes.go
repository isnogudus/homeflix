package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"time"
)

// KeyframeIndex is the stored keyframe list of a media file, valid while
// the file has the size and modification time it was read at.
type KeyframeIndex struct {
	FileID  string
	Size    int64
	ModTime time.Time
	Source  string // "matroska", "mp4" or "ffprobe"; "" when Err is set
	Times   []time.Duration
	Note    string // why the container index was not used, or what is odd
	Err     string // why no keyframes could be found
	Took    time.Duration
	MadeAt  time.Time
}

// MaxGap is the longest distance between keyframes.
func (k KeyframeIndex) MaxGap() time.Duration {
	var g time.Duration
	for i := 1; i < len(k.Times); i++ {
		g = max(g, k.Times[i]-k.Times[i-1])
	}
	return g
}

// KeyframeIndex returns the stored keyframes of a file if they were read
// at the given size and modification time.
func (s *Store) KeyframeIndex(ctx context.Context, fileID string, size int64, mtime time.Time) (KeyframeIndex, bool, error) {
	k := KeyframeIndex{FileID: fileID, Size: size}
	var blob []byte
	var mt, took, made int64
	err := s.db.QueryRowContext(ctx, `SELECT mtime, source, times, note, error, took_ms, made_at
		FROM keyframes WHERE file_id = ? AND size = ? AND mtime = ?`, fileID, size, mtime.UnixMilli()).
		Scan(&mt, &k.Source, &blob, &k.Note, &k.Err, &took, &made)
	if errors.Is(err, sql.ErrNoRows) {
		return KeyframeIndex{}, false, nil
	}
	if err != nil {
		return KeyframeIndex{}, false, err
	}
	if k.Times, err = decodeTimes(blob); err != nil {
		return KeyframeIndex{}, false, err
	}
	k.ModTime, k.MadeAt = time.UnixMilli(mt).UTC(), time.UnixMilli(made).UTC()
	k.Took = time.Duration(took) * time.Millisecond
	return k, true, nil
}

// SetKeyframeIndex stores the keyframes of a file, replacing older ones.
// Nothing is stored for a file that is no longer in the database.
func (s *Store) SetKeyframeIndex(ctx context.Context, k KeyframeIndex) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO keyframes(file_id, size, mtime, source, times, count, max_gap_ms,
			note, error, took_ms, made_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM media_files WHERE id = ?)
		ON CONFLICT(file_id) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, source = excluded.source,
			times = excluded.times, count = excluded.count, max_gap_ms = excluded.max_gap_ms, note = excluded.note,
			error = excluded.error, took_ms = excluded.took_ms, made_at = excluded.made_at`,
		k.FileID, k.Size, k.ModTime.UnixMilli(), k.Source, encodeTimes(k.Times), len(k.Times),
		k.MaxGap().Milliseconds(), k.Note, k.Err, k.Took.Milliseconds(), k.MadeAt.UnixMilli(), k.FileID)
	return err
}

// ResetKeyframes discards all stored keyframes so that they are found
// again.
func (s *Store) ResetKeyframes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM keyframes")
	return err
}

// UnindexedFiles returns the probed files of present items without
// current keyframes, neither a list nor an error. Files whose probe failed
// are left out: they are not played.
func (s *Store) UnindexedFiles(ctx context.Context) ([]MediaFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.item_id, f.version, f.part, f.path, f.size, f.mtime,
		NULL, NULL, f.probed_at, f.duration_ms, f.container, f.bit_rate
		FROM media_files f JOIN items i ON i.id = f.item_id
		LEFT JOIN keyframes k ON k.file_id = f.id AND k.size = f.size AND k.mtime = f.mtime
		WHERE f.probe IS NOT NULL AND i.missing_since IS NULL AND k.file_id IS NULL
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

// KeyframeStats counts the keyframe lists of the probed files of present
// items.
type KeyframeStats struct {
	Files    int            // probed files
	BySource map[string]int // current lists by source
	Noted    int            // current lists with a note
	Failed   int            // files whose keyframes could not be found
	Pending  int            // files without current keyframes
	Bytes    int64          // stored size of the lists
}

// Indexed is the number of files with current keyframes.
func (st KeyframeStats) Indexed() int {
	n := 0
	for _, c := range st.BySource {
		n += c
	}
	return n
}

// KeyframeStats counts the stored keyframes.
func (s *Store) KeyframeStats(ctx context.Context) (KeyframeStats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT coalesce(k.source, ''), k.file_id IS NOT NULL, coalesce(k.error, '') != '',
			coalesce(k.note, '') != '', count(*), coalesce(sum(length(k.times)), 0)
		FROM media_files f JOIN items i ON i.id = f.item_id
		LEFT JOIN keyframes k ON k.file_id = f.id AND k.size = f.size AND k.mtime = f.mtime
		WHERE f.probe IS NOT NULL AND i.missing_since IS NULL
		GROUP BY 1, 2, 3, 4`)
	if err != nil {
		return KeyframeStats{}, err
	}
	defer rows.Close()
	st := KeyframeStats{BySource: map[string]int{}}
	for rows.Next() {
		var source string
		var current, failed, noted bool
		var n int
		var bytes int64
		if err := rows.Scan(&source, &current, &failed, &noted, &n, &bytes); err != nil {
			return KeyframeStats{}, err
		}
		st.Files += n
		st.Bytes += bytes
		switch {
		case !current:
			st.Pending += n
		case failed:
			st.Failed += n
		default:
			st.BySource[source] += n
			if noted {
				st.Noted += n
			}
		}
	}
	return st, rows.Err()
}

// encodeTimes packs times as microseconds: the first as a signed varint,
// each further one as the unsigned varint of its distance to the one
// before. Times must be sorted.
func encodeTimes(times []time.Duration) []byte {
	if len(times) == 0 {
		return nil
	}
	b := make([]byte, 0, 3*len(times)+8)
	prev := times[0].Microseconds()
	b = binary.AppendVarint(b, prev)
	for _, t := range times[1:] {
		us := t.Microseconds()
		b = binary.AppendUvarint(b, uint64(max(us-prev, 0)))
		prev = max(us, prev)
	}
	return b
}

func decodeTimes(b []byte) ([]time.Duration, error) {
	if len(b) == 0 {
		return nil, nil
	}
	first, n := binary.Varint(b)
	if n <= 0 {
		return nil, errors.New("keyframes: bad encoding")
	}
	b = b[n:]
	times := []time.Duration{time.Duration(first) * time.Microsecond}
	t := first
	for len(b) > 0 {
		d, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("keyframes: bad encoding")
		}
		b = b[n:]
		t += int64(d)
		times = append(times, time.Duration(t)*time.Microsecond)
	}
	return times, nil
}
