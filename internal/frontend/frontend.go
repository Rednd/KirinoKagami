// Package frontend serves the optional browser UI for searching and
// browsing the mirror. Everything is embedded in the binary; the page talks
// to the mirror's own /api/search and /api/s endpoints.
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
	page   []byte
	static http.Handler
}

type pageData struct {
	Title            string
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

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, pageData{
		Title:            cfg.Title,
		CoverBaseURL:     cfg.CoverBaseURL,
		PreviewURL:       cfg.PreviewURL,
		DownloadsEnabled: downloadsEnabled,
		Version:          hex.EncodeToString(sum.Sum(nil))[:12],
	})
	if err != nil {
		return nil, err
	}
	return &Handler{
		page:   buf.Bytes(),
		static: http.StripPrefix("/static/", http.FileServerFS(static)),
	}, nil
}

// Index serves the page.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(h.page)
}

// Static serves /static/app.js and /static/app.css.
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
	h.static.ServeHTTP(w, r)
}
