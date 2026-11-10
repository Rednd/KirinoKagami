package frontend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kirinokagami/internal/config"
)

func newHandler(t *testing.T, downloads bool) *Handler {
	t.Helper()
	cfg := config.Default().Frontend
	cfg.Title = `Mirror <"&>`
	h, err := New(cfg, downloads)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestIndexRendersConfig(t *testing.T) {
	h := newHandler(t, false)
	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type %q", ct)
	}
	for _, want := range []string{
		`<title>Mirror &lt;&#34;&amp;&gt;</title>`, // title is escaped
		`data-cover-base="https://assets.ppy.sh/beatmaps"`,
		`data-preview-pattern="https://b.ppy.sh/preview/{id}.mp3"`, // {id} must survive unescaped
		`data-downloads="false"`,
		`/static/app.js?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestStatic(t *testing.T) {
	h := newHandler(t, true)
	for path, want := range map[string]int{
		"/static/app.js?v=abc": 200,
		"/static/app.css":      200,
		"/static/index.html":   404, // only served rendered, via Index
		"/static/missing.js":   404,
	} {
		rec := httptest.NewRecorder()
		h.Static(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	h.Static(rec, httptest.NewRequest(http.MethodGet, "/static/app.js?v=abc", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset cache-control %q", cc)
	}
}
