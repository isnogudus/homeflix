package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"homeflix/internal/config"
	"homeflix/internal/lockfile"
	"homeflix/internal/media"
	"homeflix/internal/store"
)

// CollectionType is the Jellyfin collection type of a library type.
func CollectionType(libType string) string {
	switch libType {
	case config.Music:
		return "music"
	case config.Audiobooks:
		return "books"
	}
	return "movies"
}

func (e Entry) itemType() string {
	if e.Type == "" {
		return "Movie"
	}
	return e.Type
}

// Prober reads stream information from a file.
type Prober interface {
	Probe(ctx context.Context, path string) store.ProbeResult
}

// DefaultMissingGrace is how long a vanished movie is kept, with its IDs
// and user data, before it is deleted.
const DefaultMissingGrace = 30 * 24 * time.Hour

// ErrScanRunning is returned when another scan holds the lock.
var ErrScanRunning = errors.New("a library scan is already running")

// Scanner keeps the database in step with the library directories.
type Scanner struct {
	Store        *store.Store
	Libraries    []config.Library
	Prober       Prober
	Workers      int
	SortArticles []string
	MissingGrace time.Duration
	// LockPath, if set, is locked during a scan so that only one process
	// scans a data directory at a time.
	LockPath string
	Log      *slog.Logger
	Now      func() time.Time

	mu sync.Mutex
}

// LibraryReport counts what a scan changed in one library.
type LibraryReport struct {
	Name             string
	Unit             string // what Movies counts: "movies", "tracks" or "books"
	Movies           int    // movies (tracks, books) found on disk
	Added            int
	Updated          int // title, year, IDs or location changed
	Moved            int // recognised by content or IMDb ID after a rename
	Returned         int // had been missing and are back
	Missing          int // newly missing
	FilesAdded       int
	FilesChanged     int
	FilesRemoved     int
	SidecarsChanged  int // descriptions, pictures or lyrics added, changed or removed
	UnavailablePaths []string
}

// Report is the outcome of one scan.
type Report struct {
	Libraries   []LibraryReport
	Purged      int64
	Probed      int
	ProbeFailed int
	Took        time.Duration
}

// Changed reports whether the scan changed anything in the database.
func (r Report) Changed() bool {
	if r.Purged > 0 || r.Probed > 0 || r.ProbeFailed > 0 {
		return true
	}
	for _, l := range r.Libraries {
		if l.Added+l.Updated+l.Moved+l.Returned+l.Missing+l.FilesAdded+l.FilesChanged+l.FilesRemoved+l.SidecarsChanged > 0 {
			return true
		}
	}
	return false
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Scan walks every library, applies the differences to the database,
// deletes items missing for longer than the grace period and probes new or
// changed files.
func (s *Scanner) Scan(ctx context.Context) (Report, error) {
	if !s.mu.TryLock() {
		return Report{}, ErrScanRunning
	}
	defer s.mu.Unlock()
	if s.LockPath != "" {
		unlock, err := lockfile.TryLock(s.LockPath)
		if errors.Is(err, lockfile.ErrLocked) {
			return Report{}, ErrScanRunning
		}
		if err != nil {
			return Report{}, fmt.Errorf("scan lock: %w", err)
		}
		defer unlock()
	}

	start := s.now()
	var rep Report
	for _, lib := range s.Libraries {
		lr, err := s.scanLibrary(ctx, lib)
		if err != nil {
			return rep, fmt.Errorf("library %q: %w", lib.Name, err)
		}
		rep.Libraries = append(rep.Libraries, lr)
	}
	grace := s.MissingGrace
	if grace == 0 {
		grace = DefaultMissingGrace
	}
	purged, err := s.Store.PurgeMissing(ctx, start.Add(-grace))
	if err != nil {
		return rep, fmt.Errorf("purge missing items: %w", err)
	}
	rep.Purged = purged
	rep.Probed, rep.ProbeFailed, err = s.probeAll(ctx)
	rep.Took = s.now().Sub(start)
	return rep, err
}

func (s *Scanner) scanLibrary(ctx context.Context, lib config.Library) (LibraryReport, error) {
	rep := LibraryReport{Name: lib.Name}
	dbLib, err := s.Store.EnsureLibrary(ctx, lib.Name, CollectionType(lib.Type))
	if err != nil {
		return rep, err
	}
	var entries []Entry
	var sidecars []Sidecar
	for _, root := range lib.Paths {
		var es []Entry
		var sc []Sidecar
		switch lib.Type {
		case config.Music:
			es, sc, err = WalkMusic(root, s.Log)
		case config.Audiobooks:
			es, sc, err = WalkAudiobooks(root, s.Log)
		default:
			es, err = Walk(root, s.Log)
		}
		if err != nil {
			s.Log.Error("library path unavailable; its items are left as they are", "library", lib.Name, "path", root, "err", err)
			rep.UnavailablePaths = append(rep.UnavailablePaths, root)
			continue
		}
		entries = append(entries, es...)
		sidecars = append(sidecars, sc...)
	}
	sidecarsChanged := 0
	if lib.Type == config.Music || lib.Type == config.Audiobooks {
		if sidecarsChanged, err = s.updateSidecars(ctx, dbLib.ID, sidecars, rep.UnavailablePaths); err != nil {
			return rep, fmt.Errorf("sidecar files: %w", err)
		}
	}
	existing, err := s.Store.LibraryItems(ctx, dbLib.ID)
	if err != nil {
		return rep, err
	}
	d := newDiff(dbLib.ID, existing, s.SortArticles, s.now().UTC())
	if err := d.run(entries, rep.UnavailablePaths); err != nil {
		return rep, err
	}
	d.rep.Name, d.rep.UnavailablePaths, d.rep.SidecarsChanged = lib.Name, rep.UnavailablePaths, sidecarsChanged
	switch lib.Type {
	case config.Music:
		d.rep.Unit = "tracks"
	case config.Audiobooks:
		d.rep.Unit = "books"
	default:
		d.rep.Unit = "movies"
	}
	if !d.ch.Empty() {
		if err := s.Store.ApplyScan(ctx, d.ch); err != nil {
			return rep, err
		}
	}
	return d.rep, nil
}

// diff compares the movies found on disk with the stored items of one
// library and collects the changes.
type diff struct {
	libraryID string
	articles  []string
	now       time.Time

	items        []*store.Item
	byPath       map[string]*store.Item
	fileByPath   map[string]store.MediaFile
	matched      map[string]bool // item IDs
	claimedFiles map[string]bool // file IDs

	ch  store.ScanChanges
	rep LibraryReport
}

func newDiff(libraryID string, existing []store.Item, articles []string, now time.Time) *diff {
	d := &diff{
		libraryID:    libraryID,
		articles:     articles,
		now:          now,
		byPath:       map[string]*store.Item{},
		fileByPath:   map[string]store.MediaFile{},
		matched:      map[string]bool{},
		claimedFiles: map[string]bool{},
	}
	for i := range existing {
		it := &existing[i]
		d.items = append(d.items, it)
		d.byPath[it.Path] = it
		for _, f := range it.Files {
			d.fileByPath[f.Path] = f
		}
	}
	return d
}

// signature identifies a movie's content independently of its path.
func signature(sizes []int64, mtimes []time.Time) string {
	keys := make([]string, len(sizes))
	for i := range sizes {
		keys[i] = fmt.Sprintf("%d@%d", sizes[i], mtimes[i].UnixMilli())
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func entrySignature(e Entry) string {
	sizes := make([]int64, len(e.Files))
	mtimes := make([]time.Time, len(e.Files))
	for i, f := range e.Files {
		sizes[i], mtimes[i] = f.Size, f.ModTime
	}
	return signature(sizes, mtimes)
}

func itemSignature(it *store.Item) string {
	sizes := make([]int64, len(it.Files))
	mtimes := make([]time.Time, len(it.Files))
	for i, f := range it.Files {
		sizes[i], mtimes[i] = f.Size, f.ModTime
	}
	return signature(sizes, mtimes)
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (d *diff) run(entries []Entry, unavailable []string) error {
	d.rep.Movies = len(entries)
	match := make([]*store.Item, len(entries))
	for i, e := range entries {
		if it, ok := d.byPath[e.Path]; ok && !d.matched[it.ID] {
			match[i] = it
			d.matched[it.ID] = true
		}
	}
	// Entries without a path match may be renamed or moved movies.
	bySig := map[string][]*store.Item{}
	byIMDb := map[string][]*store.Item{}
	for _, it := range d.items {
		if d.matched[it.ID] || it.ParentID != "" || len(it.Files) == 0 {
			continue
		}
		bySig[itemSignature(it)] = append(bySig[itemSignature(it)], it)
		if it.IMDbID != "" {
			byIMDb[it.IMDbID] = append(byIMDb[it.IMDbID], it)
		}
	}
	take := func(cands []*store.Item) *store.Item {
		for _, it := range cands {
			if !d.matched[it.ID] {
				d.matched[it.ID] = true
				return it
			}
		}
		return nil
	}
	for i, e := range entries {
		if match[i] != nil {
			continue
		}
		if it := take(bySig[entrySignature(e)]); it != nil {
			match[i] = it
			d.rep.Moved++
			continue
		}
		if e.Name.IMDbID != "" {
			if it := take(byIMDb[e.Name.IMDbID]); it != nil {
				match[i] = it
				d.rep.Moved++
			}
		}
	}

	for i, e := range entries {
		var err error
		if match[i] == nil {
			err = d.addMovie(e)
		} else {
			err = d.updateMovie(match[i], e)
		}
		if err != nil {
			return err
		}
	}

	// Files of matched items that were not found again are gone.
	for _, it := range d.items {
		if !d.matched[it.ID] {
			continue
		}
		for _, f := range it.Files {
			if !d.claimedFiles[f.ID] {
				d.ch.DeletedFileIDs = append(d.ch.DeletedFileIDs, f.ID)
				d.rep.FilesRemoved++
			}
		}
	}
	// Movies not found again are marked missing, unless their directory
	// could not be read at all.
	for _, it := range d.items {
		if d.matched[it.ID] || it.ParentID != "" || underAny(it.Path, unavailable) {
			continue
		}
		if it.MissingSince.IsZero() {
			upd := *it
			upd.MissingSince = d.now
			d.ch.UpdatedItems = append(d.ch.UpdatedItems, upd)
			d.rep.Missing++
		}
	}
	return nil
}

func minModTime(files []File) time.Time {
	var t time.Time
	for _, f := range files {
		if t.IsZero() || f.ModTime.Before(t) {
			t = f.ModTime
		}
	}
	return t.UTC()
}

func (d *diff) addMovie(e Entry) error {
	id, err := store.NewID()
	if err != nil {
		return err
	}
	it := store.Item{
		ID:          id,
		LibraryID:   d.libraryID,
		Type:        e.itemType(),
		Path:        e.Path,
		Title:       e.Name.Title,
		SortTitle:   SortKey(e.Name.Title, d.articles),
		Year:        e.Name.Year,
		IMDbID:      e.Name.IMDbID,
		TMDbID:      e.Name.TMDbID,
		DateCreated: minModTime(e.Files),
	}
	if it.Files, err = d.reconcileFiles(&it, e.Files, true); err != nil {
		return err
	}
	d.ch.NewItems = append(d.ch.NewItems, it)
	d.rep.Added++
	return d.reconcileExtras(id, nil, e.Extras)
}

func (d *diff) updateMovie(old *store.Item, e Entry) error {
	upd := *old
	upd.ParentID, upd.Type, upd.ExtraType = "", e.itemType(), ""
	upd.Path = e.Path
	upd.Title, upd.SortTitle = e.Name.Title, SortKey(e.Name.Title, d.articles)
	upd.Year, upd.IMDbID, upd.TMDbID = e.Name.Year, e.Name.IMDbID, e.Name.TMDbID
	upd.MissingSince = time.Time{}
	d.recordItemUpdate(old, upd)
	if _, err := d.reconcileFiles(old, e.Files, false); err != nil {
		return err
	}
	return d.reconcileExtras(old.ID, old, e.Extras)
}

// recordItemUpdate queues upd if it differs from old.
func (d *diff) recordItemUpdate(old *store.Item, upd store.Item) {
	returned := !old.MissingSince.IsZero() && upd.MissingSince.IsZero()
	fieldsChanged := upd.ParentID != old.ParentID || upd.Type != old.Type || upd.ExtraType != old.ExtraType ||
		upd.Path != old.Path || upd.Title != old.Title || upd.SortTitle != old.SortTitle ||
		upd.Year != old.Year || upd.IMDbID != old.IMDbID || upd.TMDbID != old.TMDbID
	if !fieldsChanged && upd.MissingSince.Equal(old.MissingSince) {
		return
	}
	upd.Files = nil
	d.ch.UpdatedItems = append(d.ch.UpdatedItems, upd)
	if returned {
		d.rep.Returned++
	}
	if fieldsChanged {
		d.rep.Updated++
	}
}

// reconcileFiles maps the files found for an item onto stored rows: same
// path first, then same size and modification time within the item. For a
// new item the rows to insert with it are returned; everything else is
// queued as changes.
func (d *diff) reconcileFiles(it *store.Item, found []File, isNew bool) ([]store.MediaFile, error) {
	var inserts []store.MediaFile
	for _, f := range found {
		row, ok := d.fileByPath[f.Path]
		if ok && d.claimedFiles[row.ID] {
			ok = false
		}
		if !ok && !isNew {
			for _, cand := range it.Files {
				if !d.claimedFiles[cand.ID] && cand.Size == f.Size && cand.ModTime.Equal(f.ModTime.Truncate(time.Millisecond)) {
					row, ok = cand, true
					break
				}
			}
		}
		if !ok {
			id, err := store.NewID()
			if err != nil {
				return nil, err
			}
			nf := store.MediaFile{ID: id, ItemID: it.ID, Version: f.Version, Part: f.Part, Path: f.Path,
				Size: f.Size, ModTime: f.ModTime}
			if isNew {
				inserts = append(inserts, nf)
			} else {
				d.ch.NewFiles = append(d.ch.NewFiles, nf)
			}
			d.rep.FilesAdded++
			continue
		}
		d.claimedFiles[row.ID] = true
		mtime := f.ModTime.Truncate(time.Millisecond)
		contentChanged := row.Size != f.Size || !row.ModTime.Equal(mtime)
		if row.ItemID == it.ID && row.Version == f.Version && row.Part == f.Part && row.Path == f.Path && !contentChanged {
			continue
		}
		upd := row
		upd.ItemID, upd.Version, upd.Part, upd.Path, upd.Size, upd.ModTime = it.ID, f.Version, f.Part, f.Path, f.Size, f.ModTime
		d.ch.UpdatedFiles = append(d.ch.UpdatedFiles, store.FileUpdate{File: upd, ResetProbe: contentChanged})
		if contentChanged {
			d.rep.FilesChanged++
		}
	}
	return inserts, nil
}

// reconcileExtras matches the extras found in a movie folder with the
// stored extras of the movie; extras no longer present are deleted.
func (d *diff) reconcileExtras(parentID string, old *store.Item, found []Extra) error {
	kept := map[string]bool{}
	for _, x := range found {
		upd := store.Item{
			LibraryID: d.libraryID,
			ParentID:  parentID,
			Type:      "Video",
			ExtraType: x.Type,
			Path:      x.File.Path,
			Title:     x.Title,
			SortTitle: SortKey(x.Title, d.articles),
		}
		if prev, ok := d.byPath[x.File.Path]; ok && !d.matched[prev.ID] {
			d.matched[prev.ID] = true
			kept[prev.ID] = true
			upd.ID, upd.DateCreated, upd.Year, upd.IMDbID, upd.TMDbID = prev.ID, prev.DateCreated, prev.Year, prev.IMDbID, prev.TMDbID
			d.recordItemUpdate(prev, upd)
			if _, err := d.reconcileFiles(prev, []File{x.File}, false); err != nil {
				return err
			}
			continue
		}
		id, err := store.NewID()
		if err != nil {
			return err
		}
		upd.ID, upd.DateCreated = id, x.File.ModTime.UTC()
		if upd.Files, err = d.reconcileFiles(&upd, []File{x.File}, true); err != nil {
			return err
		}
		d.ch.NewItems = append(d.ch.NewItems, upd)
	}
	if old == nil {
		return nil
	}
	for _, it := range d.items {
		if it.ParentID == old.ID && !kept[it.ID] && !d.matched[it.ID] {
			d.matched[it.ID] = true
			for _, f := range it.Files {
				d.claimedFiles[f.ID] = true
			}
			d.ch.DeletedItemIDs = append(d.ch.DeletedItemIDs, it.ID)
		}
	}
	return nil
}

// probeAll probes every file without probe data, a few at a time.
func (s *Scanner) probeAll(ctx context.Context) (probed, failed int, err error) {
	files, err := s.Store.UnprobedFiles(ctx)
	if err != nil || len(files) == 0 {
		return 0, 0, err
	}
	workers := s.Workers
	if workers < 1 {
		workers = 1
	}
	s.Log.Info("probing files", "count", len(files), "workers", workers)
	jobs := make(chan store.MediaFile)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	lastLog := s.now()
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				r := s.Prober.Probe(ctx, f.Path)
				if ctx.Err() != nil {
					return
				}
				if r.Err == "" && IsAudio(f.Path) {
					if a, err := media.ParseAudio(r.JSON); err != nil {
						r = store.ProbeResult{Err: err.Error()}
					} else if b, err := json.Marshal(a); err == nil {
						r.AudioMeta = string(b)
					}
				}
				err := s.Store.SetProbeResult(ctx, f, r, s.now())
				mu.Lock()
				switch {
				case err != nil:
					if firstErr == nil {
						firstErr = err
					}
				case r.Err != "":
					failed++
					s.Log.Warn("probe failed", "path", f.Path, "err", r.Err)
				default:
					probed++
				}
				if done := probed + failed; s.now().Sub(lastLog) >= 30*time.Second {
					lastLog = s.now()
					s.Log.Info("probing files", "done", done, "of", len(files))
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return probed, failed, firstErr
	}
	return probed, failed, ctx.Err()
}

// updateSidecars stores the sidecar files found in a library, reading the
// descriptions that are new or changed. Sidecars under paths that could
// not be read are kept as they were.
func (s *Scanner) updateSidecars(ctx context.Context, libraryID string, found []Sidecar, unavailable []string) (int, error) {
	old, err := s.Store.Sidecars(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	byPath := map[string]store.Sidecar{}
	for _, sc := range old {
		byPath[sc.Path] = sc
	}
	var list []store.Sidecar
	changed := 0
	seen := map[string]bool{}
	for _, f := range found {
		seen[f.Path] = true
		mtime := f.ModTime.Truncate(time.Millisecond).UTC()
		if prev, ok := byPath[f.Path]; ok && prev.Size == f.Size && prev.ModTime.Equal(mtime) {
			list = append(list, prev)
			continue
		}
		changed++
		sc := store.Sidecar{Path: f.Path, LibraryID: libraryID, Dir: filepath.Dir(f.Path),
			Name: strings.ToLower(filepath.Base(f.Path)), Size: f.Size, ModTime: mtime}
		data, err := ReadSidecar(f.Path)
		if err != nil {
			s.Log.Warn("unreadable description file", "path", f.Path, "err", err)
		}
		sc.Data = data
		list = append(list, sc)
	}
	for _, sc := range old {
		if seen[sc.Path] {
			continue
		}
		if underAny(sc.Path, unavailable) {
			list = append(list, sc)
			continue
		}
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, s.Store.ReplaceSidecars(ctx, libraryID, list)
}
