package search

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"kirinokagami/internal/config"
	"kirinokagami/internal/db"
	"kirinokagami/internal/models"
)

// Service answers cheesegull /api/search requests from the in-memory index,
// caching rendered responses until the next rebuild.
type Service struct {
	cfg   config.Search
	store *db.Store
	log   *slog.Logger

	idx     atomic.Pointer[Index]
	cache   *expirable.LRU[string, []byte]
	buildMu sync.Mutex

	lastBuildDur atomic.Int64
	lastBuildErr atomic.Value // string
}

func New(cfg config.Search, store *db.Store, log *slog.Logger) *Service {
	return &Service{
		cfg:   cfg,
		store: store,
		log:   log,
		cache: expirable.NewLRU[string, []byte](max(cfg.ResultCacheSize, 1), nil, cfg.ResultCacheTTL.Duration),
	}
}

// Ready reports whether an index is loaded.
func (s *Service) Ready() bool { return s.idx.Load() != nil }

// Search returns the JSON-encoded cheesegull response for p.
func (s *Service) Search(ctx context.Context, p Params) ([]byte, error) {
	p = s.normalize(p)
	key := p.cacheKey()
	if b, ok := s.cache.Get(key); ok {
		return b, nil
	}

	var ids []int
	if ix := s.idx.Load(); ix != nil {
		ids = ix.Query(p)
	} else {
		// Index not built yet (first start): fall back to the database.
		var mask uint8
		for _, m := range p.Modes {
			if m >= 0 && m < 8 {
				mask |= 1 << uint(m)
			}
		}
		var err error
		ids, err = s.store.FallbackSearch(ctx, queryTerms(p.Query), p.Status, mask, p.Amount, p.Offset)
		if err != nil {
			return nil, err
		}
	}

	sets, err := s.store.GetSets(ctx, ids)
	if err != nil {
		return nil, err
	}
	if sets == nil {
		sets = []*models.Set{}
	}
	b, err := json.Marshal(sets)
	if err != nil {
		return nil, err
	}
	if s.idx.Load() != nil {
		s.cache.Add(key, b)
	}
	return b, nil
}

func (s *Service) normalize(p Params) Params {
	if p.Amount <= 0 {
		p.Amount = s.cfg.DefaultAmount
	}
	p.Amount = min(p.Amount, s.cfg.MaxAmount)
	p.Offset = max(p.Offset, 0)
	p.Query = strings.TrimSpace(p.Query)
	p.Status = dedupSorted(p.Status)
	p.Modes = dedupSorted(p.Modes)
	return p
}

func dedupSorted(v []int) []int {
	if len(v) == 0 {
		return nil
	}
	v = slices.Clone(v)
	slices.Sort(v)
	return slices.Compact(v)
}

func (p Params) cacheKey() string {
	var sb strings.Builder
	sb.WriteString(strings.Join(queryTerms(p.Query), " "))
	sb.WriteString("|s")
	for _, x := range p.Status {
		sb.WriteString(strconv.Itoa(x) + ",")
	}
	sb.WriteString("|m")
	for _, x := range p.Modes {
		sb.WriteString(strconv.Itoa(x) + ",")
	}
	fmt.Fprintf(&sb, "|%d|%d", p.Amount, p.Offset)
	return sb.String()
}

// Rebuild reloads every set from the database into a fresh index, swaps it
// in, clears the result cache and writes a snapshot to disk.
func (s *Service) Rebuild(ctx context.Context) error {
	if !s.buildMu.TryLock() {
		s.log.Info("search index rebuild already running, skipping")
		return nil
	}
	defer s.buildMu.Unlock()

	start := time.Now()
	capHint := 0
	if old := s.idx.Load(); old != nil {
		capHint = old.Len() + 1024
	}
	entries := make([]Entry, 0, capHint)
	err := s.store.StreamSearchRows(ctx, func(r *db.SearchRow) {
		entries = append(entries, Entry{
			ID:         int32(r.ID),
			Status:     int8(r.RankedStatus),
			Modes:      r.Modes,
			Favourites: int32(r.Favourites),
			Title:      strings.ToLower(r.Title),
			Artist:     strings.ToLower(r.Artist),
			Creator:    strings.ToLower(r.Creator),
			Extra:      buildExtra(r),
		})
	})
	if err != nil {
		s.lastBuildErr.Store(err.Error())
		return fmt.Errorf("loading sets for search index: %w", err)
	}
	ix := Build(entries, time.Now().UTC())
	s.idx.Store(ix)
	s.cache.Purge()
	dur := time.Since(start)
	s.lastBuildDur.Store(int64(dur))
	s.lastBuildErr.Store("")
	s.log.Info("search index rebuilt", "sets", ix.Len(), "tokens", ix.Tokens(), "took", dur.Round(time.Millisecond))

	if err := s.saveSnapshot(ix); err != nil {
		s.log.Warn("saving search index snapshot failed", "err", err)
	}
	return nil
}

// buildExtra joins the secondary searchable fields, skipping unicode
// fields that are identical to their romanised versions.
func buildExtra(r *db.SearchRow) string {
	parts := make([]string, 0, 4)
	if r.TitleUnicode != "" && r.TitleUnicode != r.Title {
		parts = append(parts, r.TitleUnicode)
	}
	if r.ArtistUnicode != "" && r.ArtistUnicode != r.Artist {
		parts = append(parts, r.ArtistUnicode)
	}
	if r.Source != "" {
		parts = append(parts, r.Source)
	}
	if r.Tags != "" {
		parts = append(parts, r.Tags)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

type snapshot struct {
	Version int
	BuiltAt time.Time
	Entries []Entry
}

const snapshotVersion = 1

func (s *Service) saveSnapshot(ix *Index) error {
	if s.cfg.SnapshotPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.SnapshotPath), 0o755); err != nil {
		return err
	}
	tmp := s.cfg.SnapshotPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(snapshot{Version: snapshotVersion, BuiltAt: ix.builtAt, Entries: ix.entries}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.cfg.SnapshotPath)
}

// LoadSnapshot loads the index saved by the last rebuild, if any.
func (s *Service) LoadSnapshot() error {
	if s.cfg.SnapshotPath == "" {
		return nil
	}
	f, err := os.Open(s.cfg.SnapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	start := time.Now()
	var snap snapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		return fmt.Errorf("decoding search snapshot: %w", err)
	}
	if snap.Version != snapshotVersion {
		return fmt.Errorf("search snapshot version %d != %d, ignoring", snap.Version, snapshotVersion)
	}
	ix := Build(snap.Entries, snap.BuiltAt)
	s.idx.Store(ix)
	s.log.Info("loaded search index snapshot", "sets", ix.Len(), "built_at", snap.BuiltAt, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// Status is exposed on /api/status.
type Status struct {
	Ready         bool      `json:"ready"`
	Sets          int       `json:"sets"`
	Tokens        int       `json:"tokens"`
	BuiltAt       time.Time `json:"built_at,omitzero"`
	LastBuildTime string    `json:"last_build_time"`
	LastBuildErr  string    `json:"last_build_error,omitempty"`
	CachedQueries int       `json:"cached_queries"`
	RebuildCron   string    `json:"rebuild_cron"`
}

func (s *Service) Status() Status {
	st := Status{
		CachedQueries: s.cache.Len(),
		RebuildCron:   s.cfg.RebuildCron,
		LastBuildTime: time.Duration(s.lastBuildDur.Load()).Round(time.Millisecond).String(),
	}
	if e, _ := s.lastBuildErr.Load().(string); e != "" {
		st.LastBuildErr = e
	}
	if ix := s.idx.Load(); ix != nil {
		st.Ready, st.Sets, st.Tokens, st.BuiltAt = true, ix.Len(), ix.Tokens(), ix.BuiltAt()
	}
	return st
}
