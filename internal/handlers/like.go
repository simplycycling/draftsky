package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

// LikeHandler handles like/unlike actions.
type LikeHandler struct {
	app *oauth.ClientApp
}

// NewLikeHandler constructs a LikeHandler.
func NewLikeHandler(app *oauth.ClientApp) *LikeHandler {
	return &LikeHandler{app: app}
}

type likeRequest struct {
	URI string `json:"uri" binding:"required"`
	CID string `json:"cid"`
}

type unlikeRequest struct {
	URI string `json:"uri" binding:"required"`
}

// HandleCreateLike creates a like record for a post and returns an updated like-button fragment.
func (h *LikeHandler) HandleCreateLike(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	var req likeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("like: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	like := &appbsky.FeedLike{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Subject:   &comatproto.RepoStrongRef{Uri: req.URI, Cid: req.CID},
	}

	out, err := comatproto.RepoCreateRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoCreateRecord_Input{
		Collection: "app.bsky.feed.like",
		Repo:       did,
		Record:     &lexutil.LexiconTypeDecoder{Val: like},
	})
	if err != nil {
		slog.Error("like: create record failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to like post"})
		return
	}

	pv := feed.PostView{URI: req.URI, CID: req.CID, LikedByMe: true, LikeURI: out.Uri}
	c.Header("Content-Type", "text/html; charset=utf-8")
	renderLikeButton(c, pv)
}

// HandleDeleteLike removes a like record and returns an updated like-button fragment.
func (h *LikeHandler) HandleDeleteLike(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	var req unlikeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("unlike: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	rkey := rkeyFromURI(req.URI)
	if rkey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid like URI"})
		return
	}

	if _, err := comatproto.RepoDeleteRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoDeleteRecord_Input{
		Collection: "app.bsky.feed.like",
		Repo:       did,
		Rkey:       rkey,
	}); err != nil {
		slog.Error("unlike: delete record failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to unlike post"})
		return
	}

	pv := feed.PostView{LikedByMe: false}
	c.Header("Content-Type", "text/html; charset=utf-8")
	renderLikeButton(c, pv)
}

// rkeyFromURI extracts the record key from an AT URI (at://did/collection/rkey).
func rkeyFromURI(uri string) string {
	parts := strings.Split(uri, "/")
	if len(parts) < 1 {
		return ""
	}
	return parts[len(parts)-1]
}

// renderLikeButton writes the like-button HTML fragment to the response.
// Must stay in sync with the "like-button" template in feed.html.
func renderLikeButton(c *gin.Context, pv feed.PostView) {
	heart := "♡"
	action := "Like"
	hxMethod := `hx-post="/api/like"`
	hxVals := fmt.Sprintf(`hx-vals='{"uri": %q, "cid": %q}'`, pv.URI, pv.CID)
	if pv.LikedByMe {
		heart = "♥"
		action = "Unlike"
		hxMethod = `hx-delete="/api/like"`
		hxVals = fmt.Sprintf(`hx-vals='{"uri": %q}'`, pv.LikeURI)
	}
	likedClass := ""
	if pv.LikedByMe {
		likedClass = " liked"
	}
	countStr := likeCountStr(pv.LikeCount)
	html := fmt.Sprintf(
		`<span class="post-count post-count-like%s" hx-target="closest .post-count-like" hx-swap="outerHTML" %s %s hx-trigger="click" style="cursor:pointer" title="%s">%s%s</span>`,
		likedClass, hxMethod, hxVals, action, countStr, heart,
	)
	c.String(http.StatusOK, html)
}

func likeCountStr(n int64) string {
	if n <= 0 {
		return ""
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
