package store

import (
	"context"
	"testing"
	"time"
)

func TestPeople(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, p := range []Person{
		{TMDbID: 1, Status: MetaMatched, Data: `{"name":"A"}`, Photo: "1-profile-a.jpg", FetchedAt: now},
		{TMDbID: 2, Status: MetaError, Error: "boom", FetchedAt: now},
		{TMDbID: 2, Status: MetaNotFound, FetchedAt: now},
	} {
		if err := s.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AllPeople(ctx)
	if err != nil || len(all) != 2 || all[1].Photo != "1-profile-a.jpg" || !all[1].FetchedAt.Equal(now) ||
		all[2].Status != MetaNotFound || all[2].Error != "" {
		t.Fatalf("people: %+v, %v", all, err)
	}
	if err := s.DeletePeople(ctx, []int{2, 3}); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllPeople(ctx); len(all) != 1 || all[1].TMDbID != 1 {
		t.Errorf("after delete: %+v", all)
	}
}
