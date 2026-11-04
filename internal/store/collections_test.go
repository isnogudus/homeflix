package store

import (
	"context"
	"testing"
	"time"
)

func TestCollections(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, c := range []Collection{
		{TMDbID: 10, Status: MetaMatched, Data: `{"name":"Star Wars"}`, Poster: "10-collection-poster-p.jpg",
			Backdrop: "10-collection-backdrop-b.jpg", FetchedAt: now},
		{TMDbID: 11, Status: MetaError, Error: "boom", FetchedAt: now},
	} {
		if err := s.SaveCollection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AllCollections(ctx)
	if err != nil || len(all) != 2 || all[10].Backdrop != "10-collection-backdrop-b.jpg" ||
		!all[10].FetchedAt.Equal(now) || all[11].Error != "boom" {
		t.Fatalf("collections: %+v, %v", all, err)
	}
	if err := s.DeleteCollections(ctx, []int{11}); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllCollections(ctx); len(all) != 1 {
		t.Errorf("after delete: %+v", all)
	}
}
