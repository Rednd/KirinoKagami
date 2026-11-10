// Package config loads the TOML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"

	"kirinokagami/internal/models"
)

// Duration is a time.Duration that decodes from strings like "30m" or "168h".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Server    Server    `toml:"server"`
	Osu       Osu       `toml:"osu"`
	API       API       `toml:"api"`
	Database  Database  `toml:"database"`
	Scraper   Scraper   `toml:"scraper"`
	Search    Search    `toml:"search"`
	Downloads Downloads `toml:"downloads"`
	Frontend  Frontend  `toml:"frontend"`
}

type Server struct {
	Listen string `toml:"listen"`
	// AdminToken protects the /admin endpoints. Empty disables them.
	AdminToken string `toml:"admin_token"`
	// FetchMissing makes /api/s, /api/b, /api/md5 and /d fetch sets that are
	// not in the database yet straight from the osu! API.
	FetchMissing bool `toml:"fetch_missing"`
}

// Osu holds the osu! website session used for downloading .osz files.
type Osu struct {
	// SessionCookie is the osu_session cookie value of a browser that is
	// logged into osu.ppy.sh.
	SessionCookie string `toml:"session_cookie"`
	UserAgent     string `toml:"user_agent"`
}

// API holds the osu! API v2 OAuth client (https://osu.ppy.sh/home/account/edit#oauth).
type API struct {
	ClientID          int    `toml:"client_id"`
	ClientSecret      string `toml:"client_secret"`
	RequestsPerMinute int    `toml:"requests_per_minute"`
	Burst             int    `toml:"burst"`
}

type Database struct {
	// DSN overrides the individual fields when set
	// (go-sql-driver format: user:pass@tcp(host:3306)/db?parseTime=true).
	DSN             string   `toml:"dsn"`
	Host            string   `toml:"host"`
	Port            int      `toml:"port"`
	User            string   `toml:"user"`
	Password        string   `toml:"password"`
	Name            string   `toml:"name"`
	MaxOpenConns    int      `toml:"max_open_conns"`
	MaxIdleConns    int      `toml:"max_idle_conns"`
	ConnectTimeout  Duration `toml:"connect_timeout"`
	ConnectAttempts int      `toml:"connect_attempts"`
}

func (d Database) FormatDSN() string {
	if d.DSN != "" {
		return d.DSN
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=UTC&charset=utf8mb4&collation=utf8mb4_unicode_ci&timeout=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.ConnectTimeout.Duration)
}

type Scraper struct {
	Enabled bool `toml:"enabled"`
	// DiscoverInterval is how often the "newest / recently updated / recently
	// ranked" feeds are polled for new and changed maps.
	DiscoverInterval Duration `toml:"discover_interval"`
	// DiscoverMaxPages caps how many pages one feed run may walk.
	DiscoverMaxPages int `toml:"discover_max_pages"`

	// Backfill walks every beatmapset on osu! once to fill the database.
	Backfill bool `toml:"backfill"`
	// BackfillMode is "search" (page through API search, fast) or
	// "beatmap_ids" (sweep beatmap IDs 50 at a time, slower but thorough).
	BackfillMode string `toml:"backfill_mode"`

	// Refresh re-fetches stored sets so playcounts, favourites, status
	// changes and deletions are picked up.
	Refresh          bool     `toml:"refresh"`
	RefreshBatch     int      `toml:"refresh_batch"`
	RefreshRanked    Duration `toml:"refresh_ranked"`
	RefreshLoved     Duration `toml:"refresh_loved"`
	RefreshPending   Duration `toml:"refresh_pending"`
	RefreshGraveyard Duration `toml:"refresh_graveyard"`
}

// RefreshInterval returns how long a set with the given status stays fresh.
func (s Scraper) RefreshInterval(status int) time.Duration {
	switch status {
	case models.StatusRanked, models.StatusApproved:
		return s.RefreshRanked.Duration
	case models.StatusLoved:
		return s.RefreshLoved.Duration
	case models.StatusGraveyard:
		return s.RefreshGraveyard.Duration
	default: // qualified, pending, wip
		return s.RefreshPending.Duration
	}
}

type Search struct {
	// RebuildCron is a standard 5-field cron spec for rebuilding the
	// in-memory search index from the database.
	RebuildCron     string   `toml:"rebuild_cron"`
	RebuildOnStart  bool     `toml:"rebuild_on_start"`
	SnapshotPath    string   `toml:"snapshot_path"`
	ResultCacheSize int      `toml:"result_cache_size"`
	ResultCacheTTL  Duration `toml:"result_cache_ttl"`
	DefaultAmount   int      `toml:"default_amount"`
	MaxAmount       int      `toml:"max_amount"`
}

type Downloads struct {
	Enabled           bool     `toml:"enabled"`
	CacheDir          string   `toml:"cache_dir"`
	RequestsPerMinute int      `toml:"requests_per_minute"`
	Timeout           Duration `toml:"timeout"`
	// MaxCacheGB evicts the oldest .osz files once the cache grows past this
	// size. 0 disables eviction.
	MaxCacheGB float64 `toml:"max_cache_gb"`
}

// Frontend is the optional browser UI served at /.
type Frontend struct {
	Enabled bool   `toml:"enabled"`
	Title   string `toml:"title"`
	// CoverBaseURL is where cover images live; the page appends
	// /{setID}/covers/card.jpg (and cover.jpg, @2x variants). Empty hides covers.
	CoverBaseURL string `toml:"cover_base_url"`
	// PreviewURL is the audio preview URL with {id} for the set ID. Empty
	// hides the preview buttons.
	PreviewURL string `toml:"preview_url"`
}

func Default() Config {
	return Config{
		Server: Server{Listen: "0.0.0.0:62011", FetchMissing: true},
		Osu:    Osu{UserAgent: "KirinoKagami/1.0 (osu! beatmap mirror)"},
		API:    API{RequestsPerMinute: 60, Burst: 5},
		Database: Database{
			Host: "127.0.0.1", Port: 3306, User: "kirino", Name: "kirinokagami",
			MaxOpenConns: 20, MaxIdleConns: 10,
			ConnectTimeout:  Duration{10 * time.Second},
			ConnectAttempts: 30,
		},
		Scraper: Scraper{
			Enabled:          true,
			DiscoverInterval: Duration{5 * time.Minute},
			DiscoverMaxPages: 40,
			Backfill:         true,
			BackfillMode:     "search",
			Refresh:          true,
			RefreshBatch:     50,
			RefreshRanked:    Duration{7 * 24 * time.Hour},
			RefreshLoved:     Duration{7 * 24 * time.Hour},
			RefreshPending:   Duration{12 * time.Hour},
			RefreshGraveyard: Duration{60 * 24 * time.Hour},
		},
		Search: Search{
			RebuildCron:     "*/30 * * * *",
			RebuildOnStart:  true,
			SnapshotPath:    "data/search_index.gob",
			ResultCacheSize: 10000,
			ResultCacheTTL:  Duration{30 * time.Minute},
			DefaultAmount:   50,
			MaxAmount:       100,
		},
		Frontend: Frontend{
			Enabled:      false,
			Title:        "KirinoKagami",
			CoverBaseURL: "https://assets.ppy.sh/beatmaps",
			PreviewURL:   "https://b.ppy.sh/preview/{id}.mp3",
		},
		Downloads: Downloads{
			Enabled:           true,
			CacheDir:          "data/osz",
			RequestsPerMinute: 30,
			Timeout:           Duration{5 * time.Minute},
		},
	}
}

// Load reads the config file at path on top of the defaults, then applies
// environment variable overrides for secrets (handy with Docker).
func Load(path string) (Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return cfg, fmt.Errorf("%s: unknown config keys: %v", path, undec)
	}
	applyEnv(&cfg)
	return cfg, cfg.Validate()
}

func applyEnv(c *Config) {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	str("KIRINO_OSU_SESSION_COOKIE", &c.Osu.SessionCookie)
	str("KIRINO_API_CLIENT_SECRET", &c.API.ClientSecret)
	str("KIRINO_DB_DSN", &c.Database.DSN)
	str("KIRINO_DB_HOST", &c.Database.Host)
	str("KIRINO_DB_PASSWORD", &c.Database.Password)
	str("KIRINO_ADMIN_TOKEN", &c.Server.AdminToken)
	if v, ok := os.LookupEnv("KIRINO_API_CLIENT_ID"); ok {
		if id, err := strconv.Atoi(v); err == nil {
			c.API.ClientID = id
		}
	}
}

func (c Config) Validate() error {
	var errs []error
	if c.Scraper.Enabled && (c.API.ClientID == 0 || c.API.ClientSecret == "") {
		errs = append(errs, errors.New("api.client_id and api.client_secret are required when the scraper is enabled"))
	}
	if c.Scraper.BackfillMode != "search" && c.Scraper.BackfillMode != "beatmap_ids" {
		errs = append(errs, fmt.Errorf("scraper.backfill_mode must be \"search\" or \"beatmap_ids\", got %q", c.Scraper.BackfillMode))
	}
	if c.API.RequestsPerMinute <= 0 {
		errs = append(errs, errors.New("api.requests_per_minute must be > 0"))
	}
	if c.Downloads.Enabled {
		if c.Downloads.RequestsPerMinute <= 0 {
			errs = append(errs, errors.New("downloads.requests_per_minute must be > 0"))
		}
		if c.Osu.SessionCookie == "" {
			errs = append(errs, errors.New("downloads need osu.session_cookie (the osu_session cookie of a logged-in browser); set downloads.enabled = false to run metadata-only"))
		}
	}
	if c.Search.MaxAmount <= 0 || c.Search.DefaultAmount <= 0 {
		errs = append(errs, errors.New("search.default_amount and search.max_amount must be > 0"))
	}
	for _, d := range []Duration{c.Scraper.RefreshRanked, c.Scraper.RefreshLoved, c.Scraper.RefreshPending, c.Scraper.RefreshGraveyard} {
		if d.Duration <= 0 {
			errs = append(errs, errors.New("scraper.refresh_* intervals must be > 0"))
			break
		}
	}
	return errors.Join(errs...)
}
