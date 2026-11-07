package db

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"kirinokagami/internal/config"
	"kirinokagami/internal/models"
)

// Integration test; runs only when KIRINO_TEST_DSN points at a scratch DB.
func TestUpsertRoundTrip(t *testing.T) {
	dsn := os.Getenv("KIRINO_TEST_DSN")
	if dsn == "" {
		t.Skip("KIRINO_TEST_DSN not set")
	}
	ctx := context.Background()
	cfg := config.Default().Database
	cfg.DSN = dsn
	cfg.ConnectAttempts = 1
	s, err := Open(ctx, cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const id = 990001
	defer s.DeleteSet(ctx, id)

	set := &models.Set{
		ID: id, RankedStatus: 1, Artist: "xi", Title: "FREEDOM DiVE", Creator: "Nakagawa-Kanon",
		LastUpdate: time.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC), ApprovedDate: time.Date(2012, 2, 1, 0, 0, 0, 0, time.UTC),
		Genre: 2, Language: 5, GenreKnown: true, Favourites: 10,
		ChildrenBeatmaps: []models.Beatmap{
			{ID: 9900011, ParentSetID: id, DiffName: "FOUR DIMENSIONS", FileMD5: "da8aae79c8f3306b5d65ec951874a7fb", Mode: 0, MaxCombo: 2385},
			{ID: 9900012, ParentSetID: id, DiffName: "Another", Mode: 0, MaxCombo: 900},
		},
	}
	ins, err := s.UpsertSet(ctx, set, UpsertOptions{NextCheck: time.Now().Add(time.Hour), FullFetch: true})
	if err != nil || !ins {
		t.Fatalf("insert: inserted=%v err=%v", ins, err)
	}

	// Search-style update: no genre, no max combo, one diff removed.
	upd := *set
	upd.GenreKnown, upd.Genre, upd.Language = false, 0, 0
	upd.Favourites = 11
	upd.ChildrenBeatmaps = []models.Beatmap{{ID: 9900011, ParentSetID: id, DiffName: "FOUR DIMENSIONS", Mode: 0}}
	if ins, err = s.UpsertSet(ctx, &upd, UpsertOptions{NextCheck: time.Now(), KeepSchedule: true}); err != nil || ins {
		t.Fatalf("update: inserted=%v err=%v", ins, err)
	}

	got, err := s.GetSet(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Genre != 2 || got.Language != 5 {
		t.Errorf("genre/language not preserved: %d/%d", got.Genre, got.Language)
	}
	if got.Favourites != 11 || len(got.ChildrenBeatmaps) != 1 {
		t.Errorf("update not applied: favs=%d diffs=%d", got.Favourites, len(got.ChildrenBeatmaps))
	}
	bm := got.ChildrenBeatmaps[0]
	if bm.MaxCombo != 2385 || bm.FileMD5 != "da8aae79c8f3306b5d65ec951874a7fb" {
		t.Errorf("max combo / md5 not preserved: %+v", bm)
	}
	if b, _ := s.GetBeatmapByMD5(ctx, "DA8AAE79C8F3306B5D65EC951874A7FB"); b == nil || b.ID != 9900011 {
		t.Errorf("md5 lookup failed: %+v", b)
	}
	due, _ := s.DueSets(ctx, 1000)
	for _, d := range due {
		if d == id {
			t.Errorf("KeepSchedule did not keep next_check")
		}
	}
	ids, err := s.FallbackSearch(ctx, []string{"freedom"}, []int{1}, 1, 10, 0)
	if err != nil || len(ids) == 0 || ids[0] != id {
		t.Errorf("fallback search: %v %v", ids, err)
	}
}
