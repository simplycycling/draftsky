package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// fakeSettingsStore is an in-memory settingsStore for endpoint tests. It records
// the UpdateUserTheme call so tests can assert persistence without a live DB.
type fakeSettingsStore struct {
	user    db.User
	getErr  error
	updated *db.UpdateUserThemeParams // nil until UpdateUserTheme is called
}

func (f *fakeSettingsStore) GetUserByDID(_ context.Context, _ string) (db.User, error) {
	return f.user, f.getErr
}

func (f *fakeSettingsStore) UpdateUserTheme(_ context.Context, arg db.UpdateUserThemeParams) (db.User, error) {
	f.updated = &arg
	u := f.user
	u.Theme = arg.Theme
	return u, nil
}

const (
	testDID     = "did:plc:me"
	testSession = "sess_theme"
)

// themeRouter wires the real RequireCSRF middleware in front of the real handler,
// after a seed middleware that stands in for RequireAuth by populating the DID and
// session ID in context. This exercises the CSRF gate exactly as the mounted route
// does, without the session-cookie plumbing RequireAuth needs.
func themeRouter(store settingsStore, secret []byte) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSettingsHandler(store)
	seed := func(c *gin.Context) {
		c.Set(middleware.ContextKeyDID, testDID)
		c.Set(middleware.ContextKeySessionID, testSession)
	}
	r.PUT("/api/settings/theme", seed, middleware.RequireCSRF(secret), h.HandleUpdateTheme)
	return r
}

func themeRequest(secret []byte, theme string, withCSRF bool) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/api/settings/theme",
		strings.NewReader("theme="+theme))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withCSRF {
		req.Header.Set(middleware.CSRFHeaderName, auth.CSRFToken(testSession, secret))
	}
	return req
}

func TestUpdateTheme_PaidValidPersists(t *testing.T) {
	secret := []byte("theme-secret")
	store := &fakeSettingsStore{user: db.User{Did: testDID, Plan: "paid", Theme: "ocean"}}
	r := themeRouter(store, secret)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, themeRequest(secret, "amber", true))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.updated == nil {
		t.Fatal("expected UpdateUserTheme to be called")
	}
	if store.updated.Theme != "amber" {
		t.Errorf("persisted theme = %q, want amber", store.updated.Theme)
	}
	if store.updated.Did != testDID {
		t.Errorf("persisted DID = %q, want %q", store.updated.Did, testDID)
	}
}

func TestUpdateTheme_InvalidKeyBadRequest(t *testing.T) {
	secret := []byte("theme-secret")
	store := &fakeSettingsStore{user: db.User{Did: testDID, Plan: "paid", Theme: "ocean"}}
	r := themeRouter(store, secret)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, themeRequest(secret, "neon", true))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.updated != nil {
		t.Errorf("invalid key must not write: got %+v", *store.updated)
	}
}

func TestUpdateTheme_FreeUserForbidden(t *testing.T) {
	secret := []byte("theme-secret")
	// Valid theme key, but a free plan — the server must 403 regardless of the key
	// or what the UI allowed.
	store := &fakeSettingsStore{user: db.User{Did: testDID, Plan: "free", Theme: "ocean"}}
	r := themeRouter(store, secret)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, themeRequest(secret, "amber", true))

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.updated != nil {
		t.Errorf("free user must not write: got %+v", *store.updated)
	}
}

func TestUpdateTheme_MissingCSRFForbidden(t *testing.T) {
	secret := []byte("theme-secret")
	store := &fakeSettingsStore{user: db.User{Did: testDID, Plan: "paid", Theme: "ocean"}}
	r := themeRouter(store, secret)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, themeRequest(secret, "amber", false)) // no CSRF header

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.updated != nil {
		t.Errorf("request without CSRF must not reach the handler: got %+v", *store.updated)
	}
}

// TestSettingsTemplateRenders parses the settings templates exactly as production
// does and executes the full "layout" for both a free and a paid user. This catches
// template parse/execution errors — including any html/template escaping surprise in
// the inline swatch styles or the hx-vals attributes — that a code-walk cannot.
func TestSettingsTemplateRenders(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRootFromTest(t)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	h, err := NewUIHandler(nil, []byte("test-secret"), nil)
	if err != nil {
		t.Fatalf("NewUIHandler: %v", err)
	}

	for _, tc := range []struct {
		name  string
		plan  string
		theme string
	}{
		{"free", "free", "ocean"},
		{"paid", "paid", "amber"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := LayoutData{
				User:      PageUser{DID: "did:plc:me", Handle: "me.bsky.social", Plan: tc.plan},
				Theme:     tc.theme,
				CSRFToken: "tok",
			}
			if err := h.tmplSettings.ExecuteTemplate(io.Discard, "layout", data); err != nil {
				t.Fatalf("execute layout: %v", err)
			}
		})
	}
}
