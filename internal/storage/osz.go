// Package storage caches downloaded .osz archives on disk.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"kirinokagami/internal/config"
	"kirinokagami/internal/models"
	"kirinokagami/internal/osu"
)

type Downloader struct {
	cfg config.Downloads
	web *osu.Web
	log *slog.Logger
	sf  singleflight.Group
}

func NewDownloader(cfg config.Downloads, web *osu.Web, log *slog.Logger) (*Downloader, error) {
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating download cache dir: %w", err)
	}
	// Clean up partial downloads left by a crash.
	tmps, _ := filepath.Glob(filepath.Join(cfg.CacheDir, "*.tmp"))
	for _, t := range tmps {
		os.Remove(t)
	}
	return &Downloader{cfg: cfg, web: web, log: log}, nil
}

// Get returns the path of a cached, up-to-date .osz for set, downloading it
// from osu! if necessary. If the download fails but an older copy exists,
// the older copy is returned.
func (d *Downloader) Get(ctx context.Context, set *models.Set, noVideo bool) (string, error) {
	if !set.HasVideo {
		noVideo = false // both variants are identical
	}
	name := fmt.Sprintf("%d", set.ID)
	if noVideo {
		name += "n"
	}
	path := filepath.Join(d.cfg.CacheDir, name+".osz")

	fi, statErr := os.Stat(path)
	if statErr == nil && !fi.ModTime().Before(set.LastUpdate) {
		return path, nil
	}

	_, err, _ := d.sf.Do(name, func() (any, error) {
		// Detach from the request so one client disconnecting does not abort
		// a download other clients are waiting on.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.Timeout.Duration)
		defer cancel()
		return nil, d.download(dctx, set.ID, noVideo, path)
	})
	if err != nil {
		if statErr == nil && !errors.Is(err, osu.ErrUnavailable) {
			d.log.Warn("re-download failed, serving cached copy", "set", set.ID, "err", err)
			return path, nil
		}
		return "", err
	}
	return path, nil
}

func (d *Downloader) download(ctx context.Context, setID int, noVideo bool, path string) error {
	f, err := os.CreateTemp(d.cfg.CacheDir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	start := time.Now()
	n, err := d.web.Download(ctx, setID, noVideo, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	d.log.Info("downloaded beatmapset", "set", setID, "no_video", noVideo, "bytes", n, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// Janitor periodically evicts the oldest archives once the cache exceeds
// downloads.max_cache_gb.
func (d *Downloader) Janitor(ctx context.Context) {
	if d.cfg.MaxCacheGB <= 0 {
		return
	}
	limit := int64(d.cfg.MaxCacheGB * (1 << 30))
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		if err := d.evict(limit); err != nil {
			d.log.Warn("cache eviction failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Downloader) evict(limit int64) error {
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	err := filepath.WalkDir(d.cfg.CacheDir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".osz") {
			return err
		}
		info, err := de.Info()
		if err != nil {
			return nil
		}
		files = append(files, file{p, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil || total <= limit {
		return err
	}
	slices.SortFunc(files, func(a, b file) int { return a.mod.Compare(b.mod) })
	target := limit * 9 / 10 // free some headroom
	removed := 0
	for _, f := range files {
		if total <= target {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			removed++
		}
	}
	d.log.Info("evicted cached archives", "files", removed, "cache_bytes", total)
	return nil
}
