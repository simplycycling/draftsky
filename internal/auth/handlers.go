package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/db/sqlc"
)

// HandleLogin initiates the AT Protocol OAuth PKCE flow.
// Expects a `handle` query parameter (Bluesky handle or DID).
// Resolves the user's PDS, sends a PAR request, and redirects to the PDS auth page.
func (h *Handler) HandleLogin(c *gin.Context) {
	identifier := c.Query("handle")
	if identifier == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "handle query parameter is required"})
		return
	}

	redirectURL, err := h.oauthApp.StartAuthFlow(c.Request.Context(), identifier)
	if err != nil {
		slog.Error("failed to start OAuth flow", "identifier", identifier, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authentication failed"})
		return
	}

	c.Redirect(http.StatusFound, redirectURL)
}

// HandleCallback completes the AT Protocol OAuth flow.
// Exchanges the auth code for tokens, upserts the user record, and sets the
// signed session cookie before redirecting to the application root.
func (h *Handler) HandleCallback(c *gin.Context) {
	sessData, err := h.oauthApp.ProcessCallback(c.Request.Context(), c.Request.URL.Query())
	if err != nil {
		// Surface the PDS-provided error code when the AS signals an error.
		var cbErr *oauth.AuthRequestCallbackError
		if errors.As(err, &cbErr) {
			slog.Warn("OAuth callback returned error from AS",
				"code", cbErr.ErrorCode,
				"description", cbErr.ErrorDescription,
			)
			c.JSON(http.StatusUnauthorized, gin.H{"error": cbErr.ErrorCode})
			return
		}
		slog.Error("OAuth callback failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authentication callback failed"})
		return
	}

	did := string(sessData.AccountDID)

	// Resolve the handle — the identity directory caches the lookup done during
	// StartAuthFlow so this is usually a cache hit.
	var handleText pgtype.Text
	ident, err := h.oauthApp.Dir.LookupDID(c.Request.Context(), sessData.AccountDID)
	if err != nil {
		slog.Warn("could not resolve handle for DID", "did", did, "err", err)
	} else if !ident.Handle.IsInvalidHandle() {
		handleText = pgtype.Text{String: string(ident.Handle), Valid: true}
	}

	// AT Protocol token responses do not include expires_in; use a 2-hour
	// conservative estimate. Token refresh (Phase 2) will keep this accurate.
	tokenExpiry := pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour), Valid: true}

	if _, err := h.queries.UpsertUser(c.Request.Context(), db.UpsertUserParams{
		Did:          did,
		Handle:       handleText,
		AccessToken:  pgtype.Text{String: sessData.AccessToken, Valid: true},
		RefreshToken: pgtype.Text{String: sessData.RefreshToken, Valid: true},
		TokenExpiry:  tokenExpiry,
	}); err != nil {
		slog.Error("failed to upsert user after OAuth callback", "did", did, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist user session"})
		return
	}

	// Fetch the user's avatar from their PDS and persist it. Non-fatal.
	if sess, resumeErr := h.oauthApp.ResumeSession(c.Request.Context(), sessData.AccountDID, sessData.SessionID); resumeErr != nil {
		slog.Warn("could not resume session to fetch avatar", "did", did, "err", resumeErr)
	} else if profile, profileErr := appbsky.ActorGetProfile(c.Request.Context(), sess.APIClient(), did); profileErr != nil {
		slog.Warn("could not fetch profile for avatar", "did", did, "err", profileErr)
	} else if profile.Avatar != nil {
		if avatarErr := h.queries.UpdateUserAvatar(c.Request.Context(), db.UpdateUserAvatarParams{
			Did:    did,
			Avatar: pgtype.Text{String: *profile.Avatar, Valid: true},
		}); avatarErr != nil {
			slog.Warn("could not store avatar URL", "did", did, "err", avatarErr)
		}
	}

	if err := h.setSession(c.Writer, did, sessData.SessionID); err != nil {
		slog.Error("failed to set session cookie", "did", did, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}

	slog.Info("user authenticated via OAuth", "did", did)
	c.Redirect(http.StatusFound, "/")
}

// HandleLogout revokes the OAuth session on the PDS (best-effort) and clears
// the local session cookie.
func (h *Handler) HandleLogout(c *gin.Context) {
	did, sessionID, err := h.getSessionFromCookie(c.Request)
	if err == nil {
		parsedDID, parseErr := syntax.ParseDID(did)
		if parseErr == nil {
			if logoutErr := h.oauthApp.Logout(c.Request.Context(), parsedDID, sessionID); logoutErr != nil {
				// Non-fatal: the local session will be cleared regardless.
				slog.Warn("failed to revoke OAuth session on logout", "did", did, "err", logoutErr)
			}
		}
	}

	h.clearSession(c.Writer)
	c.Redirect(http.StatusFound, "/")
}

// HandleClientMetadata serves the OAuth client metadata document at the
// client_id URL. Required by the AT Protocol OAuth spec for production clients.
func (h *Handler) HandleClientMetadata(c *gin.Context) {
	meta := h.oauthApp.Config.ClientMetadata()
	c.JSON(http.StatusOK, meta)
}
