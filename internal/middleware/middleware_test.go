package middleware_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
