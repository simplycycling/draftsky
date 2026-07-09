package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	inoauth "github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/rsherman/draftsky/internal/db/sqlc"
)

const (
	SessionCookieName = "draftsky_sess"
	sessionMaxAge     = 30 * 24 * 60 * 60 // 30 days in seconds
)

type sessionPayload struct {
	DID       string `json:"did"`
	SessionID string `json:"sid"`
}

// Handler holds the dependencies for auth route handlers.
type Handler struct {
	oauthApp *inoauth.ClientApp
	queries  *db.Queries
	secret   []byte
	secure   bool // true in production; gates the Secure cookie flag
}

// NewHandler constructs an auth Handler.
// secure should be true when running behind HTTPS (i.e. APP_ENV == "production").
func NewHandler(app *inoauth.ClientApp, queries *db.Queries, secret []byte, secure bool) *Handler {
	return &Handler{
		oauthApp: app,
		queries:  queries,
		secret:   secret,
		secure:   secure,
	}
}

// ParseSessionCookie verifies the HMAC-SHA256 signature on a raw session cookie
// value and returns the DID and session ID it contains.
// Exported so middleware can call it without importing the full Handler.
func ParseSessionCookie(cookieValue string, secret []byte) (did, sessionID string, err error) {
	dotIdx := strings.LastIndex(cookieValue, ".")
	if dotIdx < 0 {
		return "", "", fmt.Errorf("malformed session cookie")
	}

	encodedPayload := cookieValue[:dotIdx]
	gotSig := cookieValue[dotIdx+1:]

	// Constant-time HMAC verification prevents timing attacks.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encodedPayload))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		return "", "", fmt.Errorf("invalid session cookie signature")
	}

	raw, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", "", fmt.Errorf("malformed session cookie payload")
	}

	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", "", fmt.Errorf("malformed session cookie data")
	}
	if p.DID == "" {
		return "", "", fmt.Errorf("missing DID in session cookie")
	}

	return p.DID, p.SessionID, nil
}

// CSRFToken derives a double-submit CSRF token from the session ID using
// HMAC-SHA256(secret, "csrf:"+sessionID), base64url encoded. It is deterministic
// per session (no server-side storage needed) and becomes invalid the moment the
// session rotates. The "csrf:" domain-separation prefix ensures the token can
// never collide with the session-cookie signature, which HMACs a different input.
func CSRFToken(sessionID string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("csrf:" + sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyCSRFToken reports whether token is the valid CSRF token for the given
// session, using a constant-time comparison to avoid timing attacks.
func VerifyCSRFToken(sessionID, token string, secret []byte) bool {
	want := CSRFToken(sessionID, secret)
	return hmac.Equal([]byte(token), []byte(want))
}

// setSession writes a signed session cookie containing the user's DID and
// the indigo session ID (needed to resume the OAuth session for API calls).
func (h *Handler) setSession(w http.ResponseWriter, did, sessionID string) error {
	p := sessionPayload{DID: did, SessionID: sessionID}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling session payload: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(raw)

	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(encodedPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    encodedPayload + "." + sig,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// clearSession expires the session cookie.
func (h *Handler) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
	})
}

// getSessionFromCookie reads and validates the session cookie from the request.
func (h *Handler) getSessionFromCookie(r *http.Request) (did, sessionID string, err error) {
	cookie, cookieErr := r.Cookie(SessionCookieName)
	if cookieErr != nil {
		return "", "", fmt.Errorf("no session cookie: %w", cookieErr)
	}
	return ParseSessionCookie(cookie.Value, h.secret)
}
