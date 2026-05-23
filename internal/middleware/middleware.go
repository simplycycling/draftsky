package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/auth"
)

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
