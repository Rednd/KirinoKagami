package osu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"kirinokagami/internal/config"
)

var (
	// ErrUnavailable means osu! refuses to serve the set (deleted, DMCA,
	// download disabled).
	ErrUnavailable = errors.New("osu: beatmapset download unavailable")
	// ErrSessionInvalid means osu.session_cookie is missing, expired, logged
	// out or not verified.
	ErrSessionInvalid      = errors.New("osu: session cookie missing or no longer valid; update osu.session_cookie in config.toml with the osu_session cookie of a logged-in browser and restart")
	ErrDownloadRateLimited = errors.New("osu: download rate limited")
)

// Web downloads .osz files from the osu! website using the osu_session
// cookie of a logged-in browser (osu.session_cookie).
type Web struct {
	cfg     config.Osu
	jar     *osuJar
	http    *http.Client
	limiter *rate.Limiter
	log     *slog.Logger
}

func NewWeb(cfg config.Osu, perMinute int, timeout time.Duration, log *slog.Logger) *Web {
	// The jar keeps the cookie scoped to osu.ppy.sh (so it never leaks to the
	// CDN the download redirects to) and picks up any refreshed value osu!
	// sends back.
	jar := &osuJar{cookies: map[string]string{}}
	if cfg.SessionCookie != "" {
		jar.set("osu_session", cfg.SessionCookie)
	}
	return &Web{
		cfg:     cfg,
		jar:     jar,
		http:    &http.Client{Jar: jar, Timeout: timeout},
		limiter: rate.NewLimiter(rate.Limit(float64(perMinute)/60), 2),
		log:     log,
	}
}

// Download streams the .osz for a set into dst.
func (w *Web) Download(ctx context.Context, setID int, noVideo bool, dst io.Writer) (int64, error) {
	if w.jar.get("osu_session") == "" {
		return 0, ErrSessionInvalid
	}
	if err := w.limiter.Wait(ctx); err != nil {
		return 0, err
	}
	u := fmt.Sprintf("%s/beatmapsets/%d/download", BaseURL, setID)
	if noVideo {
		u += "?noVideo=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", w.cfg.UserAgent)
	req.Header.Set("Referer", fmt.Sprintf("%s/beatmapsets/%d", BaseURL, setID))
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading set %d: %w", setID, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/json") {
			// osu! served a page instead of the archive: not logged in.
			w.log.Error("osu! did not accept the session cookie", "set", setID)
			return 0, ErrSessionInvalid
		}
		n, err := copyOsz(dst, resp.Body)
		if err != nil {
			return n, fmt.Errorf("downloading set %d: %w", setID, err)
		}
		return n, nil
	case http.StatusNotFound, http.StatusGone, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return 0, ErrUnavailable
	case http.StatusUnauthorized:
		w.log.Error("osu! did not accept the session cookie", "set", setID)
		return 0, ErrSessionInvalid
	case http.StatusTooManyRequests:
		return 0, ErrDownloadRateLimited
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("downloading set %d: status %d: %s", setID, resp.StatusCode, snippet(body))
	}
}

// copyOsz copies an archive, making sure it really is a zip file.
func copyOsz(dst io.Writer, src io.Reader) (int64, error) {
	br := bufio.NewReaderSize(src, 64*1024)
	magic, err := br.Peek(4)
	if err != nil {
		return 0, fmt.Errorf("reading archive: %w", err)
	}
	if string(magic[:2]) != "PK" {
		return 0, errors.New("response is not a zip archive")
	}
	return io.Copy(dst, br)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// osuJar is a minimal cookie jar that only stores cookies for osu.ppy.sh,
// so nothing leaks to the CDN the download redirects to.
type osuJar struct {
	mu      sync.Mutex
	cookies map[string]string
}

func isOsuHost(u *url.URL) bool { return u.Hostname() == "osu.ppy.sh" }

func (j *osuJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	if !isOsuHost(u) {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	for _, c := range cs {
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(now)) || c.Value == "" {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c.Value
	}
}

func (j *osuJar) Cookies(u *url.URL) []*http.Cookie {
	if !isOsuHost(u) {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.cookies))
	for k, v := range j.cookies {
		out = append(out, &http.Cookie{Name: k, Value: v})
	}
	return out
}

func (j *osuJar) get(name string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies[name]
}

func (j *osuJar) set(name, value string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies[name] = value
}
