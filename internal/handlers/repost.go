package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

// RepostHandler handles repost/un-repost actions.
type RepostHandler struct {
	app *oauth.ClientApp
}

// NewRepostHandler constructs a RepostHandler.
func NewRepostHandler(app *oauth.ClientApp) *RepostHandler {
	return &RepostHandler{app: app}
}

// HandleCreateRepost creates a repost record for a post and returns an updated repost-button fragment.
// HTMX sends hx-post as application/x-www-form-urlencoded, so we read form fields directly.
func (h *RepostHandler) HandleCreateRepost(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	uri := c.PostForm("uri")
	if uri == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "uri is required"})
		return
	}
	cid := c.PostForm("cid")

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("repost: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	repost := &appbsky.FeedRepost{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Subject:   &comatproto.RepoStrongRef{Uri: uri, Cid: cid},
	}

	out, err := comatproto.RepoCreateRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoCreateRecord_Input{
		Collection: "app.bsky.feed.repost",
		Repo:       did,
		Record:     &lexutil.LexiconTypeDecoder{Val: repost},
	})
	if err != nil {
		slog.Error("repost: create record failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to repost"})
		return
	}

	count, _ := strconv.ParseInt(c.PostForm("count"), 10, 64)
	pv := feed.PostView{URI: uri, CID: cid, RepostedByMe: true, RepostURI: out.Uri, RepostCount: count + 1}
	c.Header("Content-Type", "text/html; charset=utf-8")
	renderRepostButton(c, pv)
}

// HandleDeleteRepost removes a repost record and returns an updated repost-button fragment.
// HTMX may send hx-delete params in the body (form-encoded) or as query params depending
// on version; we check both.
func (h *RepostHandler) HandleDeleteRepost(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	repostURI := c.PostForm("repost_uri")
	if repostURI == "" {
		repostURI = c.Query("repost_uri")
	}
	if repostURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repost_uri is required"})
		return
	}

	postURI := c.PostForm("post_uri")
	if postURI == "" {
		postURI = c.Query("post_uri")
	}
	postCID := c.PostForm("post_cid")
	if postCID == "" {
		postCID = c.Query("post_cid")
	}

	parsedDID, err := syntax.ParseDID(did)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid DID"})
		return
	}

	sess, err := h.app.ResumeSession(c.Request.Context(), parsedDID, sessionID)
	if err != nil {
		slog.Error("unrepost: resume session failed", "did", did, "err", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session error"})
		return
	}

	rkey := rkeyFromURI(repostURI)
	if rkey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid repost URI"})
		return
	}

	if _, err := comatproto.RepoDeleteRecord(c.Request.Context(), sess.APIClient(), &comatproto.RepoDeleteRecord_Input{
		Collection: "app.bsky.feed.repost",
		Repo:       did,
		Rkey:       rkey,
	}); err != nil {
		slog.Error("unrepost: delete record failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to undo repost"})
		return
	}

	count, _ := strconv.ParseInt(c.PostForm("count"), 10, 64)
	if count == 0 {
		count, _ = strconv.ParseInt(c.Query("count"), 10, 64)
	}
	if count > 0 {
		count--
	}
	pv := feed.PostView{RepostedByMe: false, URI: postURI, CID: postCID, RepostCount: count}
	c.Header("Content-Type", "text/html; charset=utf-8")
	renderRepostButton(c, pv)
}

// renderRepostButton writes the repost-button HTML fragment to the response.
// Must stay in sync with the "repost-button" template in post_card.html: a menu
// trigger carrying data-* state, with onclick opening the popover built in app.js.
// data-author/data-text are intentionally omitted here — the client re-attaches them
// from the pre-toggle span so quote mode still works after a repost/undo, sparing the
// handler from echoing author/text it does not hold. AT URIs are Bluesky-issued and
// contain no double quotes, so raw interpolation into the attributes is safe.
func renderRepostButton(c *gin.Context, pv feed.PostView) {
	repostedClass := ""
	reposted := "false"
	if pv.RepostedByMe {
		repostedClass = " reposted"
		reposted = "true"
	}
	html := fmt.Sprintf(
		`<span class="post-count post-count-repost%s" data-uri="%s" data-cid="%s" data-count="%d" data-reposted="%s" data-repost-uri="%s" onclick="openRepostMenu(event, this)" style="cursor:pointer" title="Repost or quote">%s</span>`,
		repostedClass, pv.URI, pv.CID, pv.RepostCount, reposted, pv.RepostURI, countDisplayStr(pv.RepostCount),
	)
	c.String(http.StatusOK, html)
}
