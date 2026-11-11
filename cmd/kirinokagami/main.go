// Command kirinokagami runs the osu! beatmap mirror.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"kirinokagami/internal/api"
	"kirinokagami/internal/config"
	"kirinokagami/internal/db"
	"kirinokagami/internal/frontend"
	"kirinokagami/internal/osu"
	"kirinokagami/internal/scraper"
	"kirinokagami/internal/search"
	"kirinokagami/internal/storage"
)

func main() {
	cfgPath := flag.String("config", envOr("KIRINO_CONFIG", "config.toml"), "path to the config file")
	logLevel := flag.String("log-level", envOr("KIRINO_LOG_LEVEL", "info"), "debug, info, warn or error")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	if err := run(*cfgPath, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	log.Info("database ready")

	// Search index: load the snapshot first so search works immediately,
	// then rebuild in the background and on the cron schedule.
	srch := search.New(cfg.Search, store, log.With("component", "search"))
	if err := srch.LoadSnapshot(); errors.Is(err, search.ErrSnapshotOutdated) {
		log.Info("saved search index is from an older version, rebuilding it from the database")
	} else if err != nil {
		log.Warn("could not load search snapshot", "err", err)
	}
	if cfg.Search.RebuildOnStart || !srch.Ready() {
		go func() {
			if err := srch.Rebuild(ctx); err != nil {
				log.Error("initial search index build failed", "err", err)
			}
		}()
	}
	sched := cron.New()
	if _, err := sched.AddFunc(cfg.Search.RebuildCron, func() {
		if err := srch.Rebuild(ctx); err != nil {
			log.Error("scheduled search index rebuild failed", "err", err)
		}
	}); err != nil {
		return errors.New("invalid search.rebuild_cron: " + err.Error())
	}
	sched.Start()
	defer sched.Stop()

	var wg sync.WaitGroup
	defer wg.Wait()

	// Scraper / osu! API client. Created whenever API credentials exist so
	// on-demand fetches work even with the background scraper disabled.
	var sc *scraper.Scraper
	if cfg.API.ClientID != 0 && cfg.API.ClientSecret != "" {
		client := osu.NewClient(cfg.API, cfg.Osu.UserAgent, log.With("component", "osu-api"))
		sc = scraper.New(cfg.Scraper, client, store, log.With("component", "scraper"))
		if cfg.Scraper.Enabled {
			wg.Go(func() { sc.Run(ctx) })
			log.Info("scraper started", "backfill", cfg.Scraper.Backfill, "refresh", cfg.Scraper.Refresh)
		}
	} else {
		log.Warn("no osu! API credentials configured: scraper and on-demand fetching are disabled")
	}

	// Downloads.
	var dl *storage.Downloader
	if cfg.Downloads.Enabled {
		web := osu.NewWeb(cfg.Osu, cfg.Downloads.RequestsPerMinute, cfg.Downloads.Timeout.Duration, log.With("component", "osu-web"))
		if dl, err = storage.NewDownloader(cfg.Downloads, web, log.With("component", "downloads")); err != nil {
			return err
		}
		wg.Go(func() { dl.Janitor(ctx) })
	}

	var fe *frontend.Handler
	if cfg.Frontend.Enabled {
		if fe, err = frontend.New(cfg.Frontend, cfg.Downloads.Enabled); err != nil {
			return err
		}
		if logo := fe.LogoFile(); logo != "" {
			log.Info("browser frontend enabled", "path", "/", "logo", logo)
		} else {
			log.Info("browser frontend enabled", "path", "/", "logo", "built-in")
		}
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           api.New(cfg, store, srch, sc, dl, fe, log.With("component", "http")).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		stop()
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
