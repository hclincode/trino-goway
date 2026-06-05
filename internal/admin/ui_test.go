package admin_test

import (
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/config"
)

// uiBundle is a fake embedded UI bundle mirroring the Vite build layout under
// the /trino-gateway base path.
func uiBundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(`<!doctype html><html><body><div id="root"></div></body></html>`)},
		"logo.svg":                {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" data-real="1"/>`)},
		"assets/index-abc123.js":  {Data: []byte("console.log('app');")},
		"assets/index-def456.css": {Data: []byte("body{margin:0}")},
	}
}

// adminWithUI builds an Admin with the given UI bundle and all roles granted.
func adminWithUI(t *testing.T, uifs *fstest.MapFS) *admin.Admin {
	t.Helper()
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()
	cfg := admin.Config{
		Auth: config.AuthConfig{
			Type: "NOOP",
			Authorization: config.AuthorizationConfig{
				AdminRegex: ".*",
				UserRegex:  ".*",
				APIRegex:   ".*",
			},
		},
		Backends:  bs,
		History:   hs,
		Monitor:   sp,
		StatusMut: sp,
		AuthMW:    auth.Noop(),
		StartTime: time.Now(),
	}
	if uifs != nil {
		cfg.UIFS = uifs
	}
	return admin.New(cfg)
}

func TestAdmin_ServeUI_FromBundle(t *testing.T) {
	bundle := uiBundle()
	a := adminWithUI(t, &bundle)

	t.Run("serveIndex returns bundle index.html", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `<div id="root">`)
		assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
		assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	})

	t.Run("serveLogoSVG returns bundle logo", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/logo.svg", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `data-real="1"`)
		assert.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	})

	t.Run("serveAssets returns JS with immutable cache", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/assets/index-abc123.js", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "console.log")
		assert.Contains(t, rec.Header().Get("Content-Type"), "javascript")
		assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
	})

	t.Run("serveAssets returns CSS", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/assets/index-def456.css", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Header().Get("Content-Type"), "css")
	})

	t.Run("serveAssets 404 on missing asset, not SPA shell", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/assets/does-not-exist.js", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.NotContains(t, rec.Body.String(), `<div id="root">`)
	})

	t.Run("serveAssets cannot traverse outside assets", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/assets/../index.html", nil)
		// chi normalizes the path; the asset handler must never leak index.html.
		assert.NotContains(t, rec.Body.String(), `<div id="root">`)
	})
}

func TestAdmin_ServeUI_SPAFallback(t *testing.T) {
	bundle := uiBundle()
	a := adminWithUI(t, &bundle)

	deepLinks := []string{
		"/trino-gateway/dashboard",
		"/trino-gateway/cluster",
		"/trino-gateway/history",
		"/trino-gateway/routing-rules",
		"/trino-gateway/login",
		"/trino-gateway/some/nested/deep/link",
	}
	for _, link := range deepLinks {
		t.Run("deep link "+link+" serves shell", func(t *testing.T) {
			rec := do(a, http.MethodGet, link, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), `<div id="root">`,
				"deep link should fall back to index.html shell")
		})
	}
}

// TestAdmin_ServeUI_DoesNotShadowAPI is the critical invariant: the SPA wildcard
// must not capture real API/probe routes under the /trino-gateway base path.
func TestAdmin_ServeUI_DoesNotShadowAPI(t *testing.T) {
	bundle := uiBundle()
	a := adminWithUI(t, &bundle)

	t.Run("livez is not shadowed", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/livez", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), `<div id="root">`)
		assert.Equal(t, "ok", rec.Body.String())
	})

	t.Run("queryHistory API is not shadowed", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/api/queryHistory", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		// JSON array, not the HTML shell.
		assert.NotContains(t, rec.Body.String(), `<div id="root">`)
		assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	})
}

func TestAdmin_ServeUI_PlaceholderWhenNoBundle(t *testing.T) {
	a := adminWithUI(t, nil) // no UIFS

	t.Run("serveIndex falls back to placeholder", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Trino Gateway")
		assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	})

	t.Run("serveLogoSVG falls back to placeholder", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/logo.svg", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	})

	t.Run("serveAssets 404 when no bundle", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/assets/index.js", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("SPA fallback still serves placeholder shell", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/trino-gateway/dashboard", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Trino Gateway")
	})
}
