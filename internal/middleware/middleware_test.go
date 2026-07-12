package middleware_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// buildTestCookie constructs a valid signed cookie value the same way
// auth.Handler.setSession does, so we can test the middleware in isolation.
func buildTestCookie(t *testing.T, secret []byte, did, sessionID string) string {
	t.Helper()
	p := struct {
		DID       string `json:"did"`
		SessionID string `json:"sid"`
	}{DID: did, SessionID: sessionID}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s.%s", encoded, sig)
}

func TestRequireAuth_MissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/test", middleware.RequireAuth([]byte("secret")), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireAuth_TamperedCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/test", middleware.RequireAuth([]byte("secret")), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tampered.garbage"})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireAuth_ValidCookie_InjectsDID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret")
	wantDID := "did:plc:testuser123"

	r := gin.New()
	var gotDID string
	r.GET("/api/test", middleware.RequireAuth(secret), func(c *gin.Context) {
		gotDID = c.GetString(middleware.ContextKeyDID)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/test", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: buildTestCookie(t, secret, wantDID, "sess_abc"),
	})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if gotDID != wantDID {
		t.Errorf("expected DID %q in context, got %q", wantDID, gotDID)
	}
}

// csrfRouter builds a Gin engine whose /api/test route seeds the session ID into
// the context (as RequireAuth would) and then runs RequireCSRF before a 200 handler.
func csrfRouter(secret []byte, sessionID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	seed := func(c *gin.Context) { c.Set(middleware.ContextKeySessionID, sessionID) }
	grp := r.Group("/api", seed, middleware.RequireCSRF(secret))
	grp.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestRequireCSRF_ValidTokenPasses(t *testing.T) {
	secret := []byte("csrf-secret")
	sessionID := "sess_valid"
	r := csrfRouter(secret, sessionID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/test", nil)
	req.Header.Set(middleware.CSRFHeaderName, auth.CSRFToken(sessionID, secret))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", w.Code)
	}
}

func TestRequireCSRF_MissingTokenForbidden(t *testing.T) {
	secret := []byte("csrf-secret")
	r := csrfRouter(secret, "sess_missing")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 with no token, got %d", w.Code)
	}
}

func TestRequireCSRF_DifferentSessionTokenForbidden(t *testing.T) {
	secret := []byte("csrf-secret")
	r := csrfRouter(secret, "sess_current")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/test", nil)
	// Token derived from a different session must not validate.
	req.Header.Set(middleware.CSRFHeaderName, auth.CSRFToken("sess_other", secret))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 with mismatched-session token, got %d", w.Code)
	}
}

func TestRequireCSRF_GetPassesWithoutToken(t *testing.T) {
	secret := []byte("csrf-secret")
	r := csrfRouter(secret, "sess_get")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for GET without token, got %d", w.Code)
	}
}

func TestRequireCSRF_FormFieldFallback(t *testing.T) {
	secret := []byte("csrf-secret")
	sessionID := "sess_form"
	r := csrfRouter(secret, sessionID)

	form := url.Values{}
	form.Set("csrf_token", auth.CSRFToken(sessionID, secret))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with form-field token, got %d", w.Code)
	}
}

// fakePlanLookup is an in-memory middleware.PlanLookup for RequirePaidPlan tests.
type fakePlanLookup struct {
	user db.User
	err  error
}

func (f fakePlanLookup) GetUserByDID(_ context.Context, _ string) (db.User, error) {
	return f.user, f.err
}

// paidPlanRouter mounts RequirePaidPlan behind a seed middleware that stands in for
// RequireAuth by populating the DID in context.
func paidPlanRouter(lookup middleware.PlanLookup) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	seed := func(c *gin.Context) { c.Set(middleware.ContextKeyDID, "did:plc:me") }
	r.GET("/paid", seed, middleware.RequirePaidPlan(lookup), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

func TestRequirePaidPlan_PaidPasses(t *testing.T) {
	r := paidPlanRouter(fakePlanLookup{user: db.User{Did: "did:plc:me", Plan: "paid"}})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/paid", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("paid user: want 200, got %d (body %s)", w.Code, w.Body.String())
	}
}

func TestRequirePaidPlan_FreeBlocked(t *testing.T) {
	r := paidPlanRouter(fakePlanLookup{user: db.User{Did: "did:plc:me", Plan: "free"}})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/paid", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("free user: want 403, got %d (body %s)", w.Code, w.Body.String())
	}
}

// adminRouter mounts RequireAdmin on /admin/stats with the given admin DID, returning
// 200 from the handler on success.
func adminRouter(secret []byte, adminDID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/stats", middleware.RequireAdmin(secret, adminDID), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

func TestRequireAdmin_AdminDIDPasses(t *testing.T) {
	secret := []byte("admin-secret")
	adminDID := "did:plc:owner"
	r := adminRouter(secret, adminDID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/stats", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: buildTestCookie(t, secret, adminDID, "sess_admin"),
	})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("admin DID: want 200, got %d", w.Code)
	}
}

func TestRequireAdmin_OtherDID404s(t *testing.T) {
	secret := []byte("admin-secret")
	r := adminRouter(secret, "did:plc:owner")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/stats", nil)
	// A valid session — but for a different, non-owner DID.
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: buildTestCookie(t, secret, "did:plc:someoneelse", "sess_x"),
	})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("non-owner DID: want 404 (route hidden), got %d", w.Code)
	}
}

func TestRequireAdmin_NoSession404s(t *testing.T) {
	secret := []byte("admin-secret")
	r := adminRouter(secret, "did:plc:owner")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/stats", nil)
	r.ServeHTTP(w, req) // no cookie

	if w.Code != http.StatusNotFound {
		t.Errorf("no session: want 404 (not a redirect), got %d", w.Code)
	}
}

func TestRequireAdmin_UnsetAdminDID404s(t *testing.T) {
	secret := []byte("admin-secret")
	r := adminRouter(secret, "") // ADMIN_DID unset

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/stats", nil)
	// Even a valid session must not reach the handler when no admin is configured.
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: buildTestCookie(t, secret, "did:plc:owner", "sess_a"),
	})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("unset ADMIN_DID: want 404, got %d", w.Code)
	}
}

func TestRequireAuth_WrongSecretRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	signingSecret := []byte("signing-secret")
	verifySecret := []byte("different-secret")

	r := gin.New()
	r.GET("/api/test", middleware.RequireAuth(verifySecret), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/test", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: buildTestCookie(t, signingSecret, "did:plc:attacker", "sess"),
	})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
