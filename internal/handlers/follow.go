package handlers

import (
	"log/slog"
	"net/http"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/middleware"
)

// FollowHandler handles follow/unfollow actions — the app.bsky.graph.follow record
// create/delete pattern, identical in shape to likes and reposts.
type FollowHandler struct {
	app *oauth.ClientApp
}

// NewFollowHandler constructs a FollowHandler.
func NewFollowHandler(app *oauth.ClientApp) *FollowHandler {
	return &FollowHandler{app: app}
}

// HandleCreateFollow creates a follow record for the subject DID and returns the new
// follow record's AT URI as JSON (the client needs it to later unfollow). The button is
// driven by a JS fetch, so the body is form-encoded (Gotcha 2).
func (h *FollowHandler) HandleCreateFollow(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	subject := c.PostForm("did")
	if _, err := syntax.ParseDID(subject); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid subject did is required"})
		return
	}

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("follow: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	follow := &appbsky.GraphFollow{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Subject:   subject,
	}

	out, err := comatproto.RepoCreateRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoCreateRecord_Input{
		Collection: "app.bsky.graph.follow",
		Repo:       did,
		Record:     &lexutil.LexiconTypeDecoder{Val: follow},
	})
	if err != nil {
		slog.Error("follow: create record failed", "did", did, "subject", subject, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to follow"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"follow_uri": out.Uri})
}

// HandleDeleteFollow removes a follow record. HTMX-style DELETE sends params in the
// query string (Go does not parse a DELETE body via PostForm), so we check both.
func (h *FollowHandler) HandleDeleteFollow(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	followURI := c.PostForm("follow_uri")
	if followURI == "" {
		followURI = c.Query("follow_uri")
	}
	if followURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "follow_uri is required"})
		return
	}

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("unfollow: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	rkey := rkeyFromURI(followURI)
	if rkey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid follow URI"})
		return
	}

	if _, err := comatproto.RepoDeleteRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoDeleteRecord_Input{
		Collection: "app.bsky.graph.follow",
		Repo:       did,
		Rkey:       rkey,
	}); err != nil {
		slog.Error("unfollow: delete record failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to unfollow"})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}
