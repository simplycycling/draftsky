package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/rsherman/draftsky/internal/auth"
)

// SecurityHeaders adds standard security response headers to every request.
// TODO: migrate script-src and style-src to CSP nonces to remove 'unsafe-inline'.
func SecurityHeaders() gin.HandlerFunc {
	const csp = "default-src 'self'; " +
		// script-src: app.js, HTMX, and lazy-loaded hls.js — all self-hosted from
		// static/vendor. No third-party CDN: an unpkg outage or a Brave Shields /
		// ad-blocker rule blocking it would break HTMX and video, so we serve our own.
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		// img-src: avatars/thumbnails on cdn.bsky.app; https: covers video thumbnails too.
		"img-src 'self' https://cdn.bsky.app https:; " +
		// connect-src: hls.js fetches the .m3u8 playlist and .ts/.m4s segments from the
		// Bluesky video CDN (both hostnames seen in the wild).
		"connect-src 'self' https://video.bsky.app https://video.cdn.bsky.app; " +
		// media-src: blob: for the MediaSource stream hls.js feeds the <video> element;
		// https: for Safari's native HLS playing the playlist URL directly as video src.
		"media-src blob: https:; " +
		"frame-ancestors 'none'"
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Content-Security-Policy", csp)
		c.Next()
	}
}

// OperationsRateLimiter enforces a per-DID rate limit for high-frequency operations
// (likes, template CRUD). Applies a 60 requests/minute limit with a burst of 60.
type OperationsRateLimiter struct {
	limiters sync.Map // DID → *rate.Limiter
}

// NewOperationsRateLimiter constructs an OperationsRateLimiter.
func NewOperationsRateLimiter() *OperationsRateLimiter {
	return &OperationsRateLimiter{}
}

func (rl *OperationsRateLimiter) limiterFor(did string) *rate.Limiter {
	const limit = 60
	v, _ := rl.limiters.LoadOrStore(did, rate.NewLimiter(rate.Every(time.Minute/limit), limit))
	return v.(*rate.Limiter)
}

// Middleware returns a Gin HandlerFunc that enforces the per-DID rate limit.
// RequireAuth must run before this middleware to populate ContextKeyDID.
func (rl *OperationsRateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		did := c.GetString(ContextKeyDID)
		if did == "" {
			c.Next()
			return
		}
		if !rl.limiterFor(did).Allow() {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests — please wait a moment and try again."})
			return
		}
		c.Next()
	}
}

// ContextKeyDID is the Gin context key under which the authenticated user's DID
// is stored by RequireAuth. Handlers retrieve it with c.GetString(middleware.ContextKeyDID).
const ContextKeyDID = "user_did"

// ContextKeySessionID is the Gin context key under which the OAuth session ID
// is stored by RequireAuth. Required for resuming the OAuth session to make API calls.
const ContextKeySessionID = "user_session_id"

// RequireAuth validates the signed session cookie and injects the user's DID
// into the Gin context. Returns 401 JSON for missing or invalid sessions.
// Used for /api/* routes consumed by JSON clients.
func RequireAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Request.Cookie(auth.SessionCookieName)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}

		did, sessionID, err := auth.ParseSessionCookie(cookie.Value, secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}

		c.Set(ContextKeyDID, did)
		c.Set(ContextKeySessionID, sessionID)
		c.Next()
	}
}

// CSRFHeaderName is the request header carrying the double-submit CSRF token.
const CSRFHeaderName = "X-CSRF-Token"

// RequireCSRF enforces double-submit CSRF protection on state-mutating requests.
// Safe methods (GET/HEAD/OPTIONS) pass through untouched. For POST/PUT/DELETE/PATCH
// it reads the token from the X-CSRF-Token header, falling back to a csrf_token
// form field (for plain HTML form posts that cannot set a header), and verifies it
// against the session ID already placed in the context by RequireAuth/RequireSession.
// It MUST therefore run after one of those middlewares. Rejects with 403 JSON on a
// missing or invalid token.
func RequireCSRF(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		token := c.GetHeader(CSRFHeaderName)
		if token == "" {
			// PostForm only parses form-encoded/multipart bodies, so this never
			// consumes a JSON request body.
			token = c.PostForm("csrf_token")
		}

		sessionID := c.GetString(ContextKeySessionID)
		if token == "" || !auth.VerifyCSRFToken(sessionID, token, secret) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid or missing CSRF token"})
			return
		}
		c.Next()
	}
}

// RequireSession validates the signed session cookie and injects the user's DID
// into the Gin context. Redirects to /login for missing or invalid sessions.
// Used for web UI routes rendered as HTML pages.
func RequireSession(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Request.Cookie(auth.SessionCookieName)
		if err != nil {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}

		did, sessionID, err := auth.ParseSessionCookie(cookie.Value, secret)
		if err != nil {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}

		c.Set(ContextKeyDID, did)
		c.Set(ContextKeySessionID, sessionID)
		c.Next()
	}
}
