// Package scraper keeps the database in sync with osu!.
//
// Three loops share the osu! API rate limit:
//
//   - discover: polls feeds sorted by "last updated" and "ranked date" and
//     stores every new or changed set until it reaches what it saw last time.
//   - backfill: walks every beatmapset on osu! once (resumable) so the
//     database starts out complete.
//   - refresh:  re-fetches stored sets when they go stale (interval depends on
//     ranked status) to update playcounts, favourites, status changes and to
//     drop deleted sets.
package scraper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"kirinokagami/internal/config"
	"kirinokagami/internal/db"
	"kirinokagami/internal/models"
	"kirinokagami/internal/osu"
)

type Scraper struct {
	cfg   config.Scraper
	api   *osu.Client
	store *db.Store
	log   *slog.Logger

	sf     singleflight.Group
	misses *expirable.LRU[string, struct{}]

	statsMu sync.Mutex
	stats   Stats
}

// Stats are exposed on /api/status.
type Stats struct {
	LastDiscover    time.Time `json:"last_discover"`
	Discovered      int64     `json:"discovered_new"`
	Updated         int64     `json:"updated"`
	Refreshed       int64     `json:"refreshed"`
	Deleted         int64     `json:"deleted"`
	BackfillDone    bool      `json:"backfill_done"`
	BackfillPages   int64     `json:"backfill_pages"`
	LastError       string    `json:"last_error,omitempty"`
	LastErrorAt     time.Time `json:"last_error_at,omitzero"`
	OnDemandFetches int64     `json:"on_demand_fetches"`
}

func New(cfg config.Scraper, api *osu.Client, store *db.Store, log *slog.Logger) *Scraper {
	return &Scraper{
		cfg:    cfg,
		api:    api,
		store:  store,
		log:    log,
		misses: expirable.NewLRU[string, struct{}](10000, nil, 10*time.Minute),
	}
}

func (s *Scraper) Stats() Stats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.stats
}

func (s *Scraper) bump(f func(*Stats)) {
	s.statsMu.Lock()
	f(&s.stats)
	s.statsMu.Unlock()
}

func (s *Scraper) recordErr(where string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.log.Error("scraper error", "loop", where, "err", err)
	s.bump(func(st *Stats) {
		st.LastError = where + ": " + err.Error()
		st.LastErrorAt = time.Now().UTC()
	})
}

// Run starts the background loops and blocks until ctx is cancelled.
func (s *Scraper) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { s.discoverLoop(ctx) })
	if s.cfg.Backfill {
		wg.Go(func() { s.backfillLoop(ctx) })
	}
	if s.cfg.Refresh {
		wg.Go(func() { s.refreshLoop(ctx) })
	}
	wg.Wait()
}

// --- scheduling helpers ---

// nextCheckFull schedules the next refresh after a full fetch (±10% jitter).
func (s *Scraper) nextCheckFull(status int) time.Time {
	iv := s.cfg.RefreshInterval(status)
	jitter := time.Duration((rand.Float64()*0.2 - 0.1) * float64(iv))
	return time.Now().Add(iv + jitter)
}

// nextCheckSpread schedules the first refresh of a set learned from search
// results uniformly within its interval, so a large backfill does not make
// every set due at the same moment.
func (s *Scraper) nextCheckSpread(status int) time.Time {
	iv := s.cfg.RefreshInterval(status)
	return time.Now().Add(time.Duration(rand.Float64() * float64(iv)))
}

// storeFromSearch upserts a set seen in search results / feeds.
func (s *Scraper) storeFromSearch(ctx context.Context, bs *osu.Beatmapset) (bool, error) {
	set := convertSet(bs)
	return s.store.UpsertSet(ctx, set, db.UpsertOptions{
		NextCheck:    s.nextCheckSpread(set.RankedStatus),
		KeepSchedule: true,
	})
}

// storeFull upserts a set obtained from a full /beatmapsets/{id} lookup.
func (s *Scraper) storeFull(ctx context.Context, bs *osu.Beatmapset) (*models.Set, bool, error) {
	set := convertSet(bs)
	inserted, err := s.store.UpsertSet(ctx, set, db.UpsertOptions{
		NextCheck: s.nextCheckFull(set.RankedStatus),
		FullFetch: true,
	})
	return set, inserted, err
}

// --- discovery ---

type feed struct {
	name   string
	params url.Values
	key    func(*osu.Beatmapset) time.Time
}

func lastUpdated(b *osu.Beatmapset) time.Time {
	if b.LastUpdated != nil {
		return *b.LastUpdated
	}
	return time.Time{}
}

func rankedDate(b *osu.Beatmapset) time.Time {
	if b.RankedDate != nil {
		return *b.RankedDate
	}
	return time.Time{}
}

var feeds = []feed{
	{"updated", url.Values{"s": {"any"}, "sort": {"updated_desc"}}, lastUpdated},
	{"ranked", url.Values{"s": {"ranked"}, "sort": {"ranked_desc"}}, rankedDate},
	{"loved", url.Values{"s": {"loved"}, "sort": {"ranked_desc"}}, rankedDate},
	{"qualified", url.Values{"s": {"qualified"}, "sort": {"ranked_desc"}}, rankedDate},
}

func (s *Scraper) discoverLoop(ctx context.Context) {
	for {
		for _, f := range feeds {
			if err := s.runFeed(ctx, f); err != nil {
				s.recordErr("discover/"+f.name, err)
			}
		}
		s.bump(func(st *Stats) { st.LastDiscover = time.Now().UTC() })
		if err := sleep(ctx, s.cfg.DiscoverInterval.Duration); err != nil {
			return
		}
	}
}

// runFeed walks one feed (newest first) until it reaches sets older than
// the watermark stored from the previous run.
func (s *Scraper) runFeed(ctx context.Context, f feed) error {
	kvKey := "feed:" + f.name
	raw, err := s.store.GetKV(ctx, kvKey)
	if err != nil {
		return err
	}
	var watermark time.Time
	firstRun := raw == ""
	if !firstRun {
		if watermark, err = time.Parse(time.RFC3339, raw); err != nil {
			firstRun = true
		}
	}
	// Overlap a little so sets updated around the last poll are not missed.
	stopBefore := watermark.Add(-15 * time.Minute)

	maxPages := s.cfg.DiscoverMaxPages
	if firstRun {
		// With an empty watermark only take a peek; the backfill handles
		// the rest of the history.
		maxPages = min(maxPages, 4)
	}

	var newest time.Time
	var cursor string
	var nNew, nUpd int
	for page := 0; page < maxPages; page++ {
		params := cloneValues(f.params)
		params.Set("nsfw", "true")
		if cursor != "" {
			params.Set("cursor_string", cursor)
		}
		res, err := s.api.SearchBeatmapsets(ctx, params)
		if err != nil {
			return err
		}
		reachedOld := false
		for i := range res.Beatmapsets {
			bs := &res.Beatmapsets[i]
			k := f.key(bs)
			if k.After(newest) {
				newest = k
			}
			if !firstRun && k.Before(stopBefore) {
				reachedOld = true
				continue
			}
			inserted, err := s.storeFromSearch(ctx, bs)
			if err != nil {
				return err
			}
			if inserted {
				nNew++
			} else {
				nUpd++
			}
		}
		if reachedOld || res.CursorString == nil || *res.CursorString == "" || len(res.Beatmapsets) == 0 {
			break
		}
		cursor = *res.CursorString
	}

	if newest.After(watermark) {
		if err := s.store.SetKV(ctx, kvKey, newest.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if nNew > 0 || nUpd > 0 {
		s.log.Info("feed scraped", "feed", f.name, "new", nNew, "updated", nUpd)
	}
	s.bump(func(st *Stats) { st.Discovered += int64(nNew); st.Updated += int64(nUpd) })
	return nil
}

// --- backfill ---

func (s *Scraper) backfillLoop(ctx context.Context) {
	done, err := s.store.GetKV(ctx, "backfill:done:"+s.cfg.BackfillMode)
	if err != nil {
		s.recordErr("backfill", err)
	}
	if done == "1" {
		s.bump(func(st *Stats) { st.BackfillDone = true })
		return
	}
	s.log.Info("starting backfill", "mode", s.cfg.BackfillMode)
	for {
		var finished bool
		if s.cfg.BackfillMode == "beatmap_ids" {
			finished, err = s.backfillBeatmapIDsStep(ctx)
		} else {
			finished, err = s.backfillSearchStep(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.recordErr("backfill", err)
			if sleep(ctx, 30*time.Second) != nil {
				return
			}
			continue
		}
		s.bump(func(st *Stats) { st.BackfillPages++ })
		if finished {
			if err := s.store.SetKV(ctx, "backfill:done:"+s.cfg.BackfillMode, "1"); err != nil {
				s.recordErr("backfill", err)
			}
			s.bump(func(st *Stats) { st.BackfillDone = true })
			s.log.Info("backfill complete")
			return
		}
	}
}

// backfillSearchStep fetches one page of all beatmapsets ordered by last
// update (oldest first) and stores it. The cursor is persisted so a restart
// resumes where it left off.
func (s *Scraper) backfillSearchStep(ctx context.Context) (bool, error) {
	cursor, err := s.store.GetKV(ctx, "backfill:cursor")
	if err != nil {
		return false, err
	}
	params := url.Values{"s": {"any"}, "sort": {"updated_asc"}, "nsfw": {"true"}}
	if cursor != "" {
		params.Set("cursor_string", cursor)
	}
	res, err := s.api.SearchBeatmapsets(ctx, params)
	if err != nil {
		return false, err
	}
	for i := range res.Beatmapsets {
		if _, err := s.storeFromSearch(ctx, &res.Beatmapsets[i]); err != nil {
			return false, err
		}
	}
	if res.CursorString == nil || *res.CursorString == "" || len(res.Beatmapsets) == 0 {
		return true, nil
	}
	if err := s.store.SetKV(ctx, "backfill:cursor", *res.CursorString); err != nil {
		return false, err
	}
	return false, nil
}

// backfillBeatmapIDsStep sweeps 50 beatmap IDs per request. Each beatmap
// carries its beatmapset, so sets are stored partially (beatmap by
// beatmap) and completed as the sweep proceeds.
func (s *Scraper) backfillBeatmapIDsStep(ctx context.Context) (bool, error) {
	raw, err := s.store.GetKV(ctx, "backfill:beatmap_id")
	if err != nil {
		return false, err
	}
	start := 1
	if raw != "" {
		start, _ = strconv.Atoi(raw)
	}
	maxKnown, err := s.store.MaxBeatmapID(ctx)
	if err != nil {
		return false, err
	}
	if start > maxKnown+1000 && maxKnown > 0 {
		return true, nil
	}
	ids := make([]int, 50)
	for i := range ids {
		ids[i] = start + i
	}
	bms, err := s.api.GetBeatmaps(ctx, ids)
	if err != nil {
		return false, err
	}

	bySet := map[int]*models.Set{}
	var order []int
	for i := range bms {
		b := &bms[i]
		set := bySet[b.BeatmapsetID]
		if set == nil {
			if b.Beatmapset == nil {
				// No embedded set data: fetch the whole set once.
				if _, err := s.FetchSet(ctx, b.BeatmapsetID); err != nil && !errors.Is(err, osu.ErrNotFound) {
					return false, err
				}
				continue
			}
			bsCopy := *b.Beatmapset
			bsCopy.Beatmaps = nil
			set = convertSet(&bsCopy)
			bySet[b.BeatmapsetID] = set
			order = append(order, b.BeatmapsetID)
		}
		set.ChildrenBeatmaps = append(set.ChildrenBeatmaps, convertBeatmap(b, b.BeatmapsetID))
	}
	for _, id := range order {
		set := bySet[id]
		if _, err := s.store.UpsertSet(ctx, set, db.UpsertOptions{
			NextCheck:    s.nextCheckSpread(set.RankedStatus),
			KeepSchedule: true,
			Partial:      true,
		}); err != nil {
			return false, err
		}
	}
	return false, s.store.SetKV(ctx, "backfill:beatmap_id", strconv.Itoa(start+50))
}

// --- refresh ---

func (s *Scraper) refreshLoop(ctx context.Context) {
	for {
		ids, err := s.store.DueSets(ctx, s.cfg.RefreshBatch)
		if err != nil {
			s.recordErr("refresh", err)
		}
		if len(ids) == 0 {
			if sleep(ctx, time.Minute) != nil {
				return
			}
			continue
		}
		for _, id := range ids {
			if err := s.refreshSet(ctx, id); err != nil {
				if ctx.Err() != nil {
					return
				}
				s.recordErr("refresh", fmt.Errorf("set %d: %w", id, err))
				// Try again later rather than blocking the queue.
				_ = s.store.Postpone(ctx, id, time.Now().Add(time.Hour))
			}
		}
	}
}

func (s *Scraper) refreshSet(ctx context.Context, id int) error {
	bs, err := s.api.GetBeatmapset(ctx, id)
	if errors.Is(err, osu.ErrNotFound) {
		s.log.Info("beatmapset no longer exists on osu!, deleting", "set", id)
		s.bump(func(st *Stats) { st.Deleted++ })
		return s.store.DeleteSet(ctx, id)
	}
	if err != nil {
		return err
	}
	if _, _, err := s.storeFull(ctx, bs); err != nil {
		return err
	}
	s.bump(func(st *Stats) { st.Refreshed++ })
	return nil
}

// --- on-demand fetches (used by the API for sets not in the DB yet) ---

// FetchSet fetches a set from osu!, stores it and returns it.
func (s *Scraper) FetchSet(ctx context.Context, id int) (*models.Set, error) {
	return s.fetchOnce(ctx, "s:"+strconv.Itoa(id), func() (*osu.Beatmapset, error) {
		return s.api.GetBeatmapset(ctx, id)
	})
}

// FetchSetByBeatmap fetches the set containing a beatmap ID.
func (s *Scraper) FetchSetByBeatmap(ctx context.Context, beatmapID int) (*models.Set, error) {
	return s.fetchOnce(ctx, "b:"+strconv.Itoa(beatmapID), func() (*osu.Beatmapset, error) {
		return s.api.LookupBeatmapsetByBeatmap(ctx, beatmapID)
	})
}

// FetchSetByMD5 fetches the set containing a beatmap with the given checksum.
func (s *Scraper) FetchSetByMD5(ctx context.Context, md5 string) (*models.Set, error) {
	return s.fetchOnce(ctx, "md5:"+md5, func() (*osu.Beatmapset, error) {
		bm, err := s.api.LookupBeatmapByChecksum(ctx, md5)
		if err != nil {
			return nil, err
		}
		return s.api.GetBeatmapset(ctx, bm.BeatmapsetID)
	})
}

func (s *Scraper) fetchOnce(ctx context.Context, key string, get func() (*osu.Beatmapset, error)) (*models.Set, error) {
	if !s.api.Configured() {
		return nil, osu.ErrNotFound
	}
	if _, miss := s.misses.Get(key); miss {
		return nil, osu.ErrNotFound
	}
	v, err, _ := s.sf.Do(key, func() (any, error) {
		bs, err := get()
		if errors.Is(err, osu.ErrNotFound) {
			s.misses.Add(key, struct{}{})
		}
		if err != nil {
			return nil, err
		}
		set, _, err := s.storeFull(ctx, bs)
		if err != nil {
			return nil, err
		}
		s.bump(func(st *Stats) { st.OnDemandFetches++ })
		// Re-read so the response has exactly what the DB holds.
		return s.store.GetSet(ctx, set.ID)
	})
	if err != nil {
		return nil, err
	}
	set, _ := v.(*models.Set)
	if set == nil {
		return nil, osu.ErrNotFound
	}
	return set, nil
}

// RefreshNow re-fetches a set immediately (admin endpoint).
func (s *Scraper) RefreshNow(ctx context.Context, id int) error { return s.refreshSet(ctx, id) }

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
