package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
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

// PlanLookup is the narrow query surface RequirePaidPlan needs to resolve the
// authenticated user's plan. *db.Queries satisfies it.
type PlanLookup interface {
	GetUserByDID(ctx context.Context, did string) (db.User, error)
}

// RequirePaidPlan gates a route to paid-plan users. It must run AFTER RequireAuth or
// RequireSession, which populate ContextKeyDID; it loads the user by that DID and 403s
// any non-paid user with a JSON error. Not mounted on any route yet — it exists for
// future paid-only endpoints (e.g. hashtag analytics). The theme endpoint keeps its own
// inline plan check (it returns a themed 403 message), so it does not use this.
func RequirePaidPlan(lookup PlanLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		did := c.GetString(ContextKeyDID)
		if did == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}

		user, err := lookup.GetUserByDID(c.Request.Context(), did)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
				return
			}
			slog.Error("RequirePaidPlan GetUserByDID failed", "did", did, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}

		if user.Plan != "paid" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this feature requires a paid plan"})
			return
		}
		c.Next()
	}
}

// LastSeenToucher is the narrow surface TouchLastSeen needs. *db.Queries satisfies it.
type LastSeenToucher interface {
	TouchUserLastSeen(ctx context.Context, did string) error
}

// TouchLastSeen records that the authenticated user was active. It must run AFTER
// RequireAuth or RequireSession, which populate ContextKeyDID. The work happens in a
// detached goroutine with its own context (the post_history pattern) so it never
// blocks or fails the request — the request context is cancelled the moment the
// handler returns, so we deliberately do not reuse it. The once-per-hour staleness
// gate lives in the SQL (see TouchUserLastSeen), so this fires a cheap UPDATE that
// no-ops on all but the first request of each hour; there is no preceding SELECT.
func TouchLastSeen(toucher LastSeenToucher) gin.HandlerFunc {
	return func(c *gin.Context) {
		did := c.GetString(ContextKeyDID)
		if did == "" {
			c.Next()
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := toucher.TouchUserLastSeen(ctx, did); err != nil {
				slog.Warn("TouchUserLastSeen failed", "did", did, "err", err)
			}
		}()
		c.Next()
	}
}

// RequireAdmin gates a route to the single owner DID named by adminDID. It is fully
// self-contained — it validates the session cookie itself rather than sitting behind
// RequireSession — so that EVERY failure mode (no cookie, invalid cookie, valid cookie
// for a non-admin, or an unset ADMIN_DID) returns an identical bare 404. A redirect or
// a 403 would advertise that the route exists; 404 makes it indistinguishable from any
// unknown path. On success it seeds the DID and session ID into the context (as
// RequireAuth/RequireSession would) so the handler can render the layout.
func RequireAdmin(secret []byte, adminDID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if adminDID == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		cookie, err := c.Request.Cookie(auth.SessionCookieName)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		did, sessionID, err := auth.ParseSessionCookie(cookie.Value, secret)
		if err != nil || did != adminDID {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Set(ContextKeyDID, did)
		c.Set(ContextKeySessionID, sessionID)
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
