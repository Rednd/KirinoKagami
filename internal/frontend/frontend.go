// Package frontend serves the optional browser UI for searching and
// browsing the mirror. Everything is embedded in the binary except an
// optional custom logo (logo.go); the page talks to the mirror's own
// /api/search and /api/s endpoints.
package frontend

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"

	"kirinokagami/internal/config"
)

//go:embed static
var embedded embed.FS

type Handler struct {
	page     []byte
	static   http.Handler
	logo     *logo  // nil = built-in logo
	logoPath string // URL path of the custom logo
}

type pageData struct {
	Title            string
	LogoURL          string // empty = built-in logo
	LogoType         string
	CoverBaseURL     string
	PreviewURL       string
	DownloadsEnabled bool
	Version          string
}

// New renders the page once at startup with the configured values.
func New(cfg config.Frontend, downloadsEnabled bool) (*Handler, error) {
	static, err := fs.Sub(embedded, "static")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.ParseFS(static, "index.html")
	if err != nil {
		return nil, err
	}

	// Version the asset URLs by content so browsers can cache them forever
	// and still pick up new builds.
	sum := sha256.New()
	for _, name := range []string{"app.js", "app.css"} {
		b, err := fs.ReadFile(static, name)
		if err != nil {
			return nil, err
		}
		sum.Write(b)
	}

	lg, err := loadLogo()
	if err != nil {
		return nil, err
	}
	h := &Handler{
		static: http.StripPrefix("/static/", http.FileServerFS(static)),
		logo:   lg,
	}
	var logoURL, logoType string
	if lg != nil {
		// Versioned by content, so replacing the file and restarting
		// updates browsers' cached copies.
		lsum := sha256.Sum256(lg.data)
		h.logoPath = "/static/logo" + lg.ext
		logoURL = h.logoPath + "?v=" + hex.EncodeToString(lsum[:6])
		logoType = lg.ctype
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, pageData{
		Title:            cfg.Title,
		LogoURL:          logoURL,
		LogoType:         logoType,
		CoverBaseURL:     cfg.CoverBaseURL,
		PreviewURL:       cfg.PreviewURL,
		DownloadsEnabled: downloadsEnabled,
		Version:          hex.EncodeToString(sum.Sum(nil))[:12],
	})
	if err != nil {
		return nil, err
	}
	h.page = buf.Bytes()
	return h, nil
}

// LogoFile returns the custom logo's file path, or "" for the built-in logo.
func (h *Handler) LogoFile() string {
	if h.logo == nil {
		return ""
	}
	return h.logo.path
}

// Index serves the page.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(h.page)
}

// Static serves /static/app.js, /static/app.css and the custom logo.
func (h *Handler) Static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/static/index.html" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Has("v") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if h.logo != nil && r.URL.Path == h.logoPath {
		w.Header().Set("Content-Type", h.logo.ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// An SVG opened directly could otherwise run scripts on this site.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		w.Write(h.logo.data)
		return
	}
	h.static.ServeHTTP(w, r)
}
