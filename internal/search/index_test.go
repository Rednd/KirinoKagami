package search

import (
	"slices"
	"testing"
	"time"
)

func testIndex() *Index {
	return Build([]Entry{
		{ID: 1, Status: 1, Modes: 1, Favourites: 500, Title: "freedom dive", Artist: "xi", Creator: "nakagawa-kanon", Extra: "bms"},
		{ID: 2, Status: -2, Modes: 1 << 3, Favourites: 3, Title: "blue zenith", Artist: "xi", Creator: "asphyxia", Extra: ""},
		{ID: 3, Status: 4, Modes: 1 | 1<<1, Favourites: 50, Title: "kirino theme", Artist: "someone", Creator: "mapper", Extra: "東方 project dive"},
		{ID: 4, Status: 1, Modes: 1, Favourites: 10, Title: "dive into the night", Artist: "yoasobi", Creator: "x", Extra: "夜に駆ける"},
	}, time.Now())
}

func TestEmptyQueryNewestFirst(t *testing.T) {
	ix := testIndex()
	got := ix.Query(Params{Amount: 10})
	if want := []int{4, 3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got = ix.Query(Params{Amount: 2, Offset: 1})
	if want := []int{3, 2}; !slices.Equal(got, want) {
		t.Fatalf("paging: got %v want %v", got, want)
	}
}

func TestQueryRankingAndPrefix(t *testing.T) {
	ix := testIndex()
	// "div" prefix-matches dive in titles (1,4) and in tags of 3.
	got := ix.Query(Params{Query: "div", Amount: 10})
	if len(got) != 3 || got[2] != 3 {
		t.Fatalf("got %v, want title matches first and tag match (3) last", got)
	}
	got = ix.Query(Params{Query: "XI Freedom", Amount: 10})
	if want := []int{1}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := ix.Query(Params{Query: "nonexistent", Amount: 10}); len(got) != 0 {
		t.Fatalf("expected no results, got %v", got)
	}
}

func TestFilters(t *testing.T) {
	ix := testIndex()
	got := ix.Query(Params{Query: "xi", Status: []int{1}, Amount: 10})
	if want := []int{1}; !slices.Equal(got, want) {
		t.Fatalf("status filter: got %v want %v", got, want)
	}
	got = ix.Query(Params{Modes: []int{3}, Amount: 10})
	if want := []int{2}; !slices.Equal(got, want) {
		t.Fatalf("mode filter: got %v want %v", got, want)
	}
	got = ix.Query(Params{Modes: []int{1}, Status: []int{4, 1}, Amount: 10})
	if want := []int{3}; !slices.Equal(got, want) {
		t.Fatalf("combined filter: got %v want %v", got, want)
	}
}

func TestCJK(t *testing.T) {
	ix := testIndex()
	if got := ix.Query(Params{Query: "東方", Amount: 10}); !slices.Equal(got, []int{3}) {
		t.Fatalf("got %v want [3]", got)
	}
	if got := ix.Query(Params{Query: "駆ける", Amount: 10}); !slices.Equal(got, []int{4}) {
		t.Fatalf("got %v want [4]", got)
	}
}

func TestCacheKeyNormalizes(t *testing.T) {
	s := &Service{}
	s.cfg.DefaultAmount, s.cfg.MaxAmount = 50, 100
	a := s.normalize(Params{Query: "  Freedom   DIVE ", Status: []int{4, 1, 1}, Amount: 500})
	b := s.normalize(Params{Query: "freedom dive", Status: []int{1, 4}, Amount: 100})
	if a.cacheKey() != b.cacheKey() {
		t.Fatalf("keys differ: %q vs %q", a.cacheKey(), b.cacheKey())
	}
}
