package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"homeflix/internal/library"
	"homeflix/internal/store"
	"homeflix/internal/tmdb"
)

// Collection is a film series with at least two movies in the catalog.
type Collection struct {
	ID       string
	TMDbID   int
	Name     string
	Overview string
	// PosterFile and BackdropFile are local artwork of the series, "" if
	// TMDB has none or it was not fetched yet.
	PosterFile, BackdropFile string
	Movies                   []*Movie // by premiere date

	// entry stands for the collection in movie lists.
	entry *Movie
}

// CollectionsViewID is the ID of the view that holds all collections, as
// Jellyfin's "Collections" folder.
var CollectionsViewID = facetID("view", "collections")

// CollectionsView returns that view, named in the catalog's language; there
// is none without collections.
func (s *Snapshot) CollectionsView() (Library, bool) {
	if len(s.collectionList) == 0 {
		return Library{}, false
	}
	name := "Collections"
	if s.locale == "de" {
		name = "Sammlungen"
	}
	return Library{Library: store.Library{ID: CollectionsViewID, Name: name, CollectionType: "boxsets"},
		Movies: len(s.collectionList)}, true
}

// CollectionID returns the ID clients address a collection by.
func CollectionID(tmdbID int) string { return facetID("collection", strconv.Itoa(tmdbID)) }

// Entry is the collection as a list entry: a Movie of type BoxSet with
// Set pointing back here, named and dated like the series.
func (c *Collection) Entry() *Movie { return c.entry }

// buildCollections groups the movies by their film series.
func (s *Snapshot) buildCollections(opt Options) {
	s.collections = map[string]*Collection{}
	members := map[int][]*Movie{}
	names := map[int]string{}
	for _, m := range s.movies {
		if m.Meta != nil && m.Meta.Collection != nil {
			id := m.Meta.Collection.ID
			members[id] = append(members[id], m)
			names[id] = m.Meta.Collection.Name
		}
	}
	for id, ms := range members {
		if len(ms) < 2 {
			continue
		}
		c := &Collection{ID: CollectionID(id), TMDbID: id, Name: names[id], Movies: ms}
		if sc, ok := opt.Collections[id]; ok {
			c.apply(sc, opt)
		}
		sort.SliceStable(ms, func(i, j int) bool {
			if c := premiere(ms[i]).Compare(premiere(ms[j])); c != 0 {
				return c < 0
			}
			return lessByName(ms[i], ms[j])
		})
		first := ms[0]
		entry := &Movie{Item: store.Item{ID: c.ID, LibraryID: first.LibraryID, Type: "BoxSet", Title: c.Name,
			SortTitle: library.SortKey(c.Name, opt.Articles), Year: first.Year}, Set: c}
		if first.Meta != nil {
			entry.Meta = &Meta{Movie: tmdb.Movie{ReleaseDate: first.Meta.ReleaseDate}}
		}
		for _, m := range ms {
			m.Collection = c
			if m.DateCreated.After(entry.DateCreated) {
				entry.DateCreated = m.DateCreated
			}
		}
		entry.searchKey = SearchKey(c.Name)
		c.entry = entry
		s.collections[c.ID] = c
		s.collectionList = append(s.collectionList, c)
	}
	sort.Slice(s.collectionList, func(i, j int) bool {
		return lessByName(s.collectionList[i].entry, s.collectionList[j].entry)
	})
}

func (c *Collection) apply(sc store.Collection, opt Options) {
	if sc.Status != store.MetaMatched || sc.Data == "" {
		return
	}
	var d tmdb.Collection
	if err := json.Unmarshal([]byte(sc.Data), &d); err != nil {
		if opt.Log != nil {
			opt.Log.Warn("unreadable collection", "name", c.Name, "err", err)
		}
		return
	}
	if d.Name != "" {
		c.Name = d.Name
	}
	c.Overview = d.Overview
	for _, f := range []struct {
		name string
		dst  *string
	}{{sc.Poster, &c.PosterFile}, {sc.Backdrop, &c.BackdropFile}} {
		if f.name == "" || opt.MetadataDir == "" {
			continue
		}
		path := filepath.Join(opt.MetadataDir, f.name)
		if _, err := os.Stat(path); err == nil {
			*f.dst = path
		}
	}
}

// Collection looks a collection up by ID.
func (s *Snapshot) Collection(id string) (*Collection, bool) {
	c, ok := s.collections[id]
	return c, ok
}

// Collections lists all collections by name.
func (s *Snapshot) Collections() []*Collection { return s.collectionList }

// InLibrary reports whether a movie of the collection is in a library.
func (c *Collection) InLibrary(libraryID string) bool {
	for _, m := range c.Movies {
		if m.LibraryID == libraryID {
			return true
		}
	}
	return false
}

// DateAdded is when the newest movie of the collection was added.
func (c *Collection) DateAdded() time.Time { return c.entry.DateCreated }
