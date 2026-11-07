// Package api serves the cheesegull-compatible HTTP API:
//
//	GET /api/b/{id}       single beatmap
//	GET /api/md5/{md5}    single beatmap by .osu checksum
//	GET /api/s/{id}       beatmapset with ChildrenBeatmaps
//	GET /api/search       search (query, amount, offset, status, mode)
//	GET /d/{id}[n]        download .osz ("n" suffix = no video)
//
// plus /api/status and token-protected /admin endpoints.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kirinokagami/internal/config"
	"kirinokagami/internal/db"
	"kirinokagami/internal/models"
	"kirinokagami/internal/osu"
	"kirinokagami/internal/scraper"
	"kirinokagami/internal/search"
	"kirinokagami/internal/storage"
)

type Server struct {
	cfg        config.Config
	store      *db.Store
	search     *search.Service
	scraper    *scraper.Scraper    // may be nil
	downloader *storage.Downloader // may be nil
	web        *osu.Web            // may be nil
	log        *slog.Logger
	started    time.Time
}

func New(cfg config.Config, store *db.Store, srch *search.Service, sc *scraper.Scraper,
	dl *storage.Downloader, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, search: srch, scraper: sc, downloader: dl, log: log, started: time.Now()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api", s.index)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/b/{id}", s.beatmap)
	mux.HandleFunc("GET /api/md5/{md5}", s.beatmapMD5)
	mux.HandleFunc("GET /api/s/{id}", s.set)
	mux.HandleFunc("GET /api/search", s.searchHandler)
	mux.HandleFunc("GET /d/{id}", s.download)

	mux.HandleFunc("POST /admin/search/rebuild", s.admin(s.adminRebuild))
	mux.HandleFunc("POST /admin/sets/{id}/refresh", s.admin(s.adminRefresh))
	return s.logRequests(mux)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next.ServeHTTP(rec, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.RequestURI(), "status", rec.code, "took", time.Since(start))
	})
}

// --- info ---

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "KirinoKagami - cheesegull-compatible osu! beatmap mirror")
	fmt.Fprintln(w, "endpoints: /api/b/{id} /api/md5/{md5} /api/s/{id} /api/search /d/{id} /api/status")
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"uptime": time.Since(s.started).Round(time.Second).String(),
		"search": s.search.Status(),
	}
	if sets, bms, err := s.store.Stats(r.Context()); err == nil {
		out["database"] = map[string]int{"sets": sets, "beatmaps": bms}
	} else {
		out["database"] = map[string]string{"error": err.Error()}
	}
	if s.scraper != nil {
		out["scraper"] = s.scraper.Stats()
	}
	writeJSON(w, http.StatusOK, out)
}

// --- metadata ---

func (s *Server) lookupCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 20*time.Second)
}

func (s *Server) canFetch() bool { return s.cfg.Server.FetchMissing && s.scraper != nil }

func (s *Server) beatmap(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid beatmap id")
		return
	}
	ctx, cancel := s.lookupCtx(r)
	defer cancel()
	bm, err := s.store.GetBeatmap(ctx, id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if bm == nil && s.canFetch() {
		if set, ferr := s.scraper.FetchSetByBeatmap(ctx, id); ferr == nil {
			bm = findBeatmap(set, func(b *models.Beatmap) bool { return b.ID == id })
		} else if !errors.Is(ferr, osu.ErrNotFound) {
			s.log.Warn("on-demand fetch failed", "beatmap", id, "err", ferr)
		}
	}
	if bm == nil {
		writeJSON(w, http.StatusNotFound, nil)
		return
	}
	writeJSON(w, http.StatusOK, bm)
}

var md5Re = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func (s *Server) beatmapMD5(w http.ResponseWriter, r *http.Request) {
	md5 := strings.ToLower(r.PathValue("md5"))
	if !md5Re.MatchString(md5) {
		writeErr(w, http.StatusBadRequest, "invalid md5")
		return
	}
	ctx, cancel := s.lookupCtx(r)
	defer cancel()
	bm, err := s.store.GetBeatmapByMD5(ctx, md5)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if bm == nil && s.canFetch() {
		if set, ferr := s.scraper.FetchSetByMD5(ctx, md5); ferr == nil {
			bm = findBeatmap(set, func(b *models.Beatmap) bool { return b.FileMD5 == md5 })
		} else if !errors.Is(ferr, osu.ErrNotFound) {
			s.log.Warn("on-demand fetch failed", "md5", md5, "err", ferr)
		}
	}
	if bm == nil {
		writeJSON(w, http.StatusNotFound, nil)
		return
	}
	writeJSON(w, http.StatusOK, bm)
}

func findBeatmap(set *models.Set, pred func(*models.Beatmap) bool) *models.Beatmap {
	if set == nil {
		return nil
	}
	for i := range set.ChildrenBeatmaps {
		if pred(&set.ChildrenBeatmaps[i]) {
			return &set.ChildrenBeatmaps[i]
		}
	}
	return nil
}

// getSet loads a set from the DB, fetching it from osu! if allowed.
func (s *Server) getSet(ctx context.Context, id int) (*models.Set, error) {
	set, err := s.store.GetSet(ctx, id)
	if err != nil || set != nil || !s.canFetch() {
		return set, err
	}
	set, err = s.scraper.FetchSet(ctx, id)
	if errors.Is(err, osu.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		s.log.Warn("on-demand fetch failed", "set", id, "err", err)
		return nil, nil
	}
	return set, nil
}

func (s *Server) set(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid set id")
		return
	}
	ctx, cancel := s.lookupCtx(r)
	defer cancel()
	set, err := s.getSet(ctx, id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if set == nil {
		writeJSON(w, http.StatusNotFound, nil)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// --- search ---

// intList parses repeated (?status=1&status=2) and comma-separated
// (?status=1,2) integer parameters.
func intList(vals []string) []int {
	var out []int
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func (s *Server) searchHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := search.Params{
		Query:  q.Get("query"),
		Status: intList(q["status"]),
		Modes:  intList(q["mode"]),
	}
	if p.Query == "" {
		p.Query = q.Get("q")
	}
	p.Amount, _ = strconv.Atoi(q.Get("amount"))
	p.Offset, _ = strconv.Atoi(q.Get("offset"))

	b, err := s.search.Search(r.Context(), p)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	writeRaw(w, b)
}

// --- downloads ---

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if s.downloader == nil {
		writeErr(w, http.StatusServiceUnavailable, "downloads are disabled on this mirror")
		return
	}
	raw := r.PathValue("id")
	raw = strings.TrimSuffix(raw, ".osz")
	noVideo := false
	if strings.HasSuffix(raw, "n") {
		noVideo = true
		raw = strings.TrimSuffix(raw, "n")
	}
	q := r.URL.Query()
	if q.Has("n") || q.Get("novideo") == "1" || q.Get("noVideo") == "1" || q.Get("novideo") == "true" {
		noVideo = true
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid set id")
		return
	}

	ctx, cancel := s.lookupCtx(r)
	set, err := s.getSet(ctx, id)
	cancel()
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if set == nil {
		writeErr(w, http.StatusNotFound, "beatmapset not found")
		return
	}

	path, err := s.downloader.Get(r.Context(), set, noVideo)
	switch {
	case err == nil:
	case errors.Is(err, osu.ErrUnavailable):
		writeErr(w, http.StatusNotFound, "beatmapset is not available for download on osu!")
		return
	case errors.Is(err, osu.ErrSessionInvalid), errors.Is(err, osu.ErrDownloadRateLimited):
		s.log.Warn("download failed", "set", id, "err", err)
		writeErr(w, http.StatusServiceUnavailable, "mirror cannot download from osu! right now")
		return
	default:
		s.log.Error("download failed", "set", id, "err", err)
		writeErr(w, http.StatusBadGateway, "downloading from osu! failed")
		return
	}

	f, err := os.Open(path)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalErr(w, err)
		return
	}
	filename := fmt.Sprintf("%d %s - %s.osz", set.ID, set.Artist, set.Title)
	w.Header().Set("Content-Type", "application/x-osu-beatmap-archive")
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// contentDisposition builds an attachment header with an ASCII fallback
// and an RFC 5987 UTF-8 filename.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || strings.ContainsRune(`"\/:*?<>|`, r) {
			return '_'
		}
		return r
	}, name)
	enc := strings.ReplaceAll(urlPathEscape(name), "+", "%20")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, enc)
}

func urlPathEscape(s string) string {
	var sb strings.Builder
	for _, b := range []byte(s) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.IndexByte("-._~", b) >= 0 {
			sb.WriteByte(b)
		} else {
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

// --- admin ---

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := s.cfg.Server.AdminToken
		if tok == "" {
			http.NotFound(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		h(w, r)
	}
}

func (s *Server) adminRebuild(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.search.Rebuild(context.Background()); err != nil {
			s.log.Error("search rebuild failed", "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rebuild started"})
}

func (s *Server) adminRefresh(w http.ResponseWriter, r *http.Request) {
	if s.scraper == nil {
		writeErr(w, http.StatusServiceUnavailable, "scraper is disabled")
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid set id")
		return
	}
	if err := s.scraper.RefreshNow(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	set, err := s.store.GetSet(r.Context(), id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) internalErr(w http.ResponseWriter, err error) {
	s.log.Error("internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal server error")
}
