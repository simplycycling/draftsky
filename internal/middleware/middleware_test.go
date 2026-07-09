package middleware_test

import (
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
