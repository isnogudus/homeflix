package store

import (
	"context"
	"testing"
	"time"
)

func TestUserData(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	u, _ := s.CreateUser(ctx, "anna", "h", false)
	other, _ := s.CreateUser(ctx, "bert", "h", false)

	if d, err := s.GetUserData(ctx, u.ID, "m1"); err != nil || !d.IsZero() || d.ItemID != "m1" {
		t.Fatalf("untouched item: %+v, %v", d, err)
	}
	played := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	if err := s.SaveUserData(ctx, u.ID, UserData{ItemID: "m1", Position: 42 * time.Minute, LastPlayed: played}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUserData(ctx, u.ID, UserData{ItemID: "m2", IsFavorite: true}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetUserData(ctx, u.ID, "m1")
	if d.Position != 42*time.Minute || !d.LastPlayed.Equal(played) || d.Played {
		t.Errorf("m1: %+v", d)
	}
	all, err := s.AllUserData(ctx, u.ID)
	if err != nil || len(all) != 2 || !all["m2"].IsFavorite {
		t.Errorf("all: %+v, %v", all, err)
	}
	if theirs, _ := s.AllUserData(ctx, other.ID); len(theirs) != 0 {
		t.Errorf("state leaked to another user: %+v", theirs)
	}
	// Saving an empty state removes the row.
	if err := s.SaveUserData(ctx, u.ID, UserData{ItemID: "m2"}); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllUserData(ctx, u.ID); len(all) != 1 {
		t.Errorf("empty state kept: %+v", all)
	}
	// State goes away with the user.
	s.DeleteUser(ctx, u.ID)
	if all, _ := s.AllUserData(ctx, u.ID); len(all) != 0 {
		t.Errorf("state survived user deletion: %+v", all)
	}
}
