package frontend

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

// A 1x1 PNG.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB`\x82")

func render(t *testing.T, h *Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}

func TestBuiltInLogoWithoutFile(t *testing.T) {
	t.Chdir(t.TempDir()) // no branding/ folder here
	h, err := New(config.Default().Frontend, false)
	if err != nil {
		t.Fatal(err)
	}
	page := render(t, h)
	if h.LogoFile() != "" || !strings.Contains(page, `class="brand-mark"`) || !strings.Contains(page, `href="data:image/svg+xml`) {
		t.Errorf("expected built-in logo and favicon")
	}
}

func TestLogoAutoDetectedFromBrandingDir(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Mkdir(DefaultLogoDir, 0o755)
	os.WriteFile(DefaultLogoDir+"/logo.png", tinyPNG, 0o644)

	h, err := New(config.Default().Frontend, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.LogoFile() != "branding/logo.png" {
		t.Fatalf("LogoFile = %q", h.LogoFile())
	}
	page := render(t, h)
	src := regexp.MustCompile(`<img class="brand-logo" src="(/static/logo\.png\?v=[0-9a-f]+)"`).FindStringSubmatch(page)
	if src == nil {
		t.Fatalf("header logo missing:\n%s", page)
	}
	if !strings.Contains(page, `<link rel="icon" href="`+src[1]+`" type="image/png">`) {
		t.Errorf("favicon does not use the custom logo")
	}
	if strings.Contains(page, `class="brand-mark"`) {
		t.Errorf("built-in logo still shown")
	}

	rec := httptest.NewRecorder()
	h.Static(rec, httptest.NewRequest(http.MethodGet, src[1], nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), tinyPNG) {
		t.Errorf("serving logo: status %d type %q len %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
}

func TestSVGLogoAndSizeLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Mkdir(DefaultLogoDir, 0o755)
	os.WriteFile(DefaultLogoDir+"/logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	h, err := New(config.Default().Frontend, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Static(rec, httptest.NewRequest(http.MethodGet, "/static/logo.svg", nil))
	if rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("svg headers: %v", rec.Header())
	}

	os.WriteFile(DefaultLogoDir+"/logo.svg", make([]byte, maxLogoBytes+1), 0o644)
	if _, err := New(config.Default().Frontend, false); err == nil {
		t.Errorf("oversized logo: expected an error")
	}
}
