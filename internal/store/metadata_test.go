package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMetadata(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	lib, _ := s.EnsureLibrary(ctx, "Filme", "movies")
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.ApplyScan(ctx, ScanChanges{NewItems: []Item{
		{ID: "m1", LibraryID: lib.ID, Type: "Movie", Path: "/m/A.mkv", Title: "A", SortTitle: "a", DateCreated: now},
		{ID: "x1", LibraryID: lib.ID, ParentID: "m1", Type: "Video", Path: "/m/x.mkv", Title: "X", SortTitle: "x", DateCreated: now},
	}}); err != nil {
		t.Fatal(err)
	}
	movies, err := s.Movies(ctx)
	if err != nil || len(movies) != 1 || movies[0].ID != "m1" {
		t.Fatalf("movies: %+v, %v", movies, err)
	}
	if _, err := s.GetMetadata(ctx, "m1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no metadata yet: %v", err)
	}
	m := Metadata{ItemID: "m1", Source: "imdb", MatchKey: "tt1|a|0", Status: MetaMatched, TMDbID: 42,
		Data: `{"title":"A"}`, Poster: "42-p.jpg", FetchedAt: now}
	if err := s.SaveMetadata(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Status, m.Error = MetaError, "boom"
	if err := s.SaveMetadata(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMetadata(ctx, "m1")
	if err != nil || got.Status != MetaError || got.TMDbID != 42 || !got.FetchedAt.Equal(now) {
		t.Errorf("metadata: %+v, %v", got, err)
	}
	all, _ := s.AllMetadata(ctx)
	if len(all) != 1 {
		t.Errorf("all: %+v", all)
	}
	s.DeleteMetadata(ctx, "m1")
	if all, _ := s.AllMetadata(ctx); len(all) != 0 {
		t.Errorf("after delete: %+v", all)
	}
}
