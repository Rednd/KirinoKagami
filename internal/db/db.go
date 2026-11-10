// Package db stores beatmap data in MySQL / MariaDB.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"kirinokagami/internal/config"
	"kirinokagami/internal/models"
)

//go:embed schema.sql
var schema string

type Store struct {
	db *sql.DB
}

// Open connects to the database, retrying while it comes up (useful when
// the database container starts at the same time as the mirror).
func Open(ctx context.Context, cfg config.Database, log *slog.Logger) (*Store, error) {
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(30 * time.Minute)

	attempts := max(cfg.ConnectAttempts, 1)
	for i := 1; ; i++ {
		pctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout.Duration)
		err = db.PingContext(pctx)
		cancel()
		if err == nil {
			break
		}
		if i >= attempts {
			db.Close()
			return nil, fmt.Errorf("connecting to database: %w", err)
		}
		log.Warn("database not reachable yet, retrying", "attempt", i, "err", err)
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Migrate creates the tables if they do not exist.
func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrating: %w", err)
		}
	}
	return nil
}

// UpsertOptions controls how UpsertSet merges with existing rows.
type UpsertOptions struct {
	// NextCheck is when the refresher should next re-fetch this set.
	NextCheck time.Time
	// KeepSchedule leaves next_check untouched for existing rows (it is
	// still written for new rows).
	KeepSchedule bool
	// FullFetch marks the row as checked now (last_checked = now).
	FullFetch bool
	// Partial means set.ChildrenBeatmaps is not the complete list: siblings
	// are not deleted and the mode mask is OR-ed into the existing one.
	Partial bool
}

// UpsertSet inserts or updates a set and its beatmaps. It reports whether
// the set was newly inserted.
func (s *Store) UpsertSet(ctx context.Context, set *models.Set, o UpsertOptions) (inserted bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var lastChecked any
	if o.FullFetch {
		lastChecked = time.Now().UTC()
	}
	var genre, language any
	if set.GenreKnown {
		genre, language = set.Genre, set.Language
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO sets (id, ranked_status, approved_date, last_update, last_checked, next_check,
    artist, artist_unicode, title, title_unicode, creator, creator_id, source, tags,
    has_video, has_storyboard, genre, language, favourites, modes)
VALUES (?,?,?,?,?,?, ?,?,?,?,?,?,?,?, ?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE
    ranked_status = VALUES(ranked_status),
    approved_date = VALUES(approved_date),
    last_update = VALUES(last_update),
    last_checked = COALESCE(VALUES(last_checked), last_checked),
    next_check = IF(?, next_check, VALUES(next_check)),
    artist = VALUES(artist), artist_unicode = VALUES(artist_unicode),
    title = VALUES(title), title_unicode = VALUES(title_unicode),
    creator = VALUES(creator), creator_id = VALUES(creator_id),
    source = VALUES(source), tags = VALUES(tags),
    has_video = VALUES(has_video), has_storyboard = VALUES(has_storyboard),
    genre = COALESCE(VALUES(genre), genre),
    language = COALESCE(VALUES(language), language),
    favourites = VALUES(favourites),
    modes = IF(?, modes | VALUES(modes), VALUES(modes))`,
		set.ID, set.RankedStatus, nullTime(set.ApprovedDate), set.LastUpdate.UTC(), lastChecked, o.NextCheck.UTC(),
		set.Artist, set.ArtistUnicode, set.Title, set.TitleUnicode, set.Creator, set.CreatorID, set.Source, set.Tags,
		set.HasVideo, set.HasStoryboard, genre, language, set.Favourites, set.ModeMask(),
		o.KeepSchedule, o.Partial,
	)
	if err != nil {
		return false, fmt.Errorf("upserting set %d: %w", set.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		inserted = true
	}

	if len(set.ChildrenBeatmaps) > 0 {
		if err := upsertBeatmaps(ctx, tx, set.ChildrenBeatmaps); err != nil {
			return false, fmt.Errorf("upserting beatmaps of set %d: %w", set.ID, err)
		}
	}
	if !o.Partial {
		q := "DELETE FROM beatmaps WHERE parent_set_id = ?"
		args := []any{set.ID}
		if len(set.ChildrenBeatmaps) > 0 {
			q += " AND id NOT IN (" + placeholders(len(set.ChildrenBeatmaps)) + ")"
			for _, b := range set.ChildrenBeatmaps {
				args = append(args, b.ID)
			}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return false, fmt.Errorf("pruning beatmaps of set %d: %w", set.ID, err)
		}
	}
	return inserted, tx.Commit()
}

func upsertBeatmaps(ctx context.Context, tx *sql.Tx, bms []models.Beatmap) error {
	const cols = 16
	var sb strings.Builder
	sb.WriteString(`INSERT INTO beatmaps (id, parent_set_id, diff_name, file_md5, mode, bpm, ar, od, cs, hp,
    total_length, hit_length, playcount, passcount, max_combo, difficulty_rating) VALUES `)
	args := make([]any, 0, len(bms)*cols)
	for i, b := range bms {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(" + placeholders(cols) + ")")
		args = append(args, b.ID, b.ParentSetID, b.DiffName, b.FileMD5, b.Mode, b.BPM, b.AR, b.OD, b.CS, b.HP,
			b.TotalLength, b.HitLength, b.Playcount, b.Passcount, b.MaxCombo, b.DifficultyRating)
	}
	sb.WriteString(` ON DUPLICATE KEY UPDATE
    parent_set_id = VALUES(parent_set_id), diff_name = VALUES(diff_name),
    file_md5 = IF(VALUES(file_md5) <> '', VALUES(file_md5), file_md5),
    mode = VALUES(mode), bpm = VALUES(bpm), ar = VALUES(ar), od = VALUES(od), cs = VALUES(cs), hp = VALUES(hp),
    total_length = VALUES(total_length), hit_length = VALUES(hit_length),
    playcount = VALUES(playcount), passcount = VALUES(passcount),
    max_combo = IF(VALUES(max_combo) > 0, VALUES(max_combo), max_combo),
    difficulty_rating = VALUES(difficulty_rating)`)
	_, err := tx.ExecContext(ctx, sb.String(), args...)
	return err
}

// DeleteSet removes a set (e.g. deleted from osu!) and its beatmaps.
func (s *Store) DeleteSet(ctx context.Context, id int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM beatmaps WHERE parent_set_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sets WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// Postpone pushes a set's next check back, used when a refresh fails so a
// broken set does not block the refresh queue.
func (s *Store) Postpone(ctx context.Context, id int, until time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sets SET next_check = ? WHERE id = ?", until.UTC(), id)
	return err
}

const setColumns = `id, ranked_status, approved_date, last_update, COALESCE(last_checked, last_update),
    artist, title, creator, source, tags, has_video, COALESCE(genre, 0), COALESCE(language, 0), favourites`

func scanSet(sc interface{ Scan(...any) error }) (*models.Set, error) {
	var s models.Set
	var approved sql.NullTime
	err := sc.Scan(&s.ID, &s.RankedStatus, &approved, &s.LastUpdate, &s.LastChecked,
		&s.Artist, &s.Title, &s.Creator, &s.Source, &s.Tags, &s.HasVideo, &s.Genre, &s.Language, &s.Favourites)
	if err != nil {
		return nil, err
	}
	if approved.Valid {
		s.ApprovedDate = approved.Time
	}
	s.ChildrenBeatmaps = []models.Beatmap{}
	return &s, nil
}

const beatmapColumns = `id, parent_set_id, diff_name, file_md5, mode, bpm, ar, od, cs, hp,
    total_length, hit_length, playcount, passcount, max_combo, difficulty_rating`

func scanBeatmap(sc interface{ Scan(...any) error }) (models.Beatmap, error) {
	var b models.Beatmap
	err := sc.Scan(&b.ID, &b.ParentSetID, &b.DiffName, &b.FileMD5, &b.Mode, &b.BPM, &b.AR, &b.OD, &b.CS, &b.HP,
		&b.TotalLength, &b.HitLength, &b.Playcount, &b.Passcount, &b.MaxCombo, &b.DifficultyRating)
	return b, err
}

// GetSet returns a set with its beatmaps, or nil if it does not exist.
func (s *Store) GetSet(ctx context.Context, id int) (*models.Set, error) {
	sets, err := s.GetSets(ctx, []int{id})
	if err != nil || len(sets) == 0 {
		return nil, err
	}
	return sets[0], nil
}

// GetSets returns the given sets with their beatmaps, in the order of ids.
// Missing ids are skipped.
func (s *Store) GetSets(ctx context.Context, ids []int) ([]*models.Set, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	ph := placeholders(len(ids))

	rows, err := s.db.QueryContext(ctx, "SELECT "+setColumns+" FROM sets WHERE id IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*models.Set, len(ids))
	for rows.Next() {
		set, err := scanSet(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[set.ID] = set
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, "SELECT "+beatmapColumns+" FROM beatmaps WHERE parent_set_id IN ("+ph+
		") ORDER BY parent_set_id, mode, difficulty_rating", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBeatmap(rows)
		if err != nil {
			return nil, err
		}
		if set := byID[b.ParentSetID]; set != nil {
			set.ChildrenBeatmaps = append(set.ChildrenBeatmaps, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*models.Set, 0, len(byID))
	for _, id := range ids {
		if set := byID[id]; set != nil {
			out = append(out, set)
		}
	}
	return out, nil
}

// GetBeatmap returns a beatmap by ID, or nil.
func (s *Store) GetBeatmap(ctx context.Context, id int) (*models.Beatmap, error) {
	return s.oneBeatmap(ctx, "SELECT "+beatmapColumns+" FROM beatmaps WHERE id = ?", id)
}

// GetBeatmapByMD5 returns a beatmap by its .osu file checksum, or nil.
func (s *Store) GetBeatmapByMD5(ctx context.Context, md5 string) (*models.Beatmap, error) {
	return s.oneBeatmap(ctx, "SELECT "+beatmapColumns+" FROM beatmaps WHERE file_md5 = ? LIMIT 1", strings.ToLower(md5))
}

func (s *Store) oneBeatmap(ctx context.Context, q string, arg any) (*models.Beatmap, error) {
	b, err := scanBeatmap(s.db.QueryRowContext(ctx, q, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// DueSets returns IDs of sets whose next_check has passed, oldest first.
func (s *Store) DueSets(ctx context.Context, limit int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM sets WHERE next_check <= ? ORDER BY next_check LIMIT ?", time.Now().UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MaxBeatmapID returns the highest stored beatmap ID.
func (s *Store) MaxBeatmapID(ctx context.Context) (int, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT MAX(id) FROM beatmaps").Scan(&id)
	return int(id.Int64), err
}

// GetKV reads a key from the kv table ("" if missing).
func (s *Store) GetKV(ctx context.Context, k string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = ?", k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(ctx context.Context, k, v string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO kv (k, v) VALUES (?, ?) ON DUPLICATE KEY UPDATE v = VALUES(v)", k, v)
	return err
}

// Stats returns row counts.
func (s *Store) Stats(ctx context.Context) (sets, beatmaps int, err error) {
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sets").Scan(&sets); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM beatmaps").Scan(&beatmaps)
	return
}

// SearchRow is the subset of a set needed by the search index.
type SearchRow struct {
	ID            int
	RankedStatus  int
	ApprovedDate  sql.NullTime
	Modes         uint8
	Favourites    int
	Artist        string
	ArtistUnicode string
	Title         string
	TitleUnicode  string
	Creator       string
	Source        string
	Tags          string
}

// StreamSearchRows calls fn for every set, highest ID first.
func (s *Store) StreamSearchRows(ctx context.Context, fn func(*SearchRow)) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ranked_status, approved_date, modes, favourites,
    artist, artist_unicode, title, title_unicode, creator, source, tags FROM sets ORDER BY id DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var r SearchRow
	for rows.Next() {
		if err := rows.Scan(&r.ID, &r.RankedStatus, &r.ApprovedDate, &r.Modes, &r.Favourites, &r.Artist, &r.ArtistUnicode,
			&r.Title, &r.TitleUnicode, &r.Creator, &r.Source, &r.Tags); err != nil {
			return err
		}
		fn(&r)
	}
	return rows.Err()
}

// FallbackSearch runs a (slow) LIKE-based search directly against the
// database. It is only used while the search index is not built yet. The
// order matches the index: see search.Index.Query.
func (s *Store) FallbackSearch(ctx context.Context, terms []string, statuses []int, modeMask uint8, amount, offset int) ([]int, error) {
	q := "SELECT id FROM sets WHERE 1=1"
	var args []any
	if len(statuses) > 0 {
		q += " AND ranked_status IN (" + placeholders(len(statuses)) + ")"
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	if modeMask != 0 {
		q += " AND (modes & ?) <> 0"
		args = append(args, modeMask)
	}
	for _, t := range terms {
		q += " AND CONCAT_WS(' ', artist, artist_unicode, title, title_unicode, creator, source, tags) LIKE ?"
		args = append(args, "%"+escapeLike(t)+"%")
	}
	if len(terms) == 0 && models.OnlyDatedStatuses(statuses) {
		// Most recently ranked / loved / qualified first.
		q += " ORDER BY approved_date IS NULL, approved_date DESC, id DESC LIMIT ? OFFSET ?"
	} else {
		q += " ORDER BY id DESC LIMIT ? OFFSET ?"
	}
	args = append(args, amount, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
