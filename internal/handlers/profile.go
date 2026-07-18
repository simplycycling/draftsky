package handlers

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/gin-gonic/gin"

	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

// Profile field limits. Bluesky's app.bsky.actor.profile lexicon caps displayName at
// 64 graphemes and description at 256 graphemes. We validate by rune count, which is
// always >= grapheme count, so a value passing this check can never exceed Bluesky's
// grapheme cap — the same rune-based approach the template name/suffix validation uses.
const (
	profileDisplayNameLimit = 64
	profileDescriptionLimit = 256
)

// isValidActor reports whether s is a syntactically plausible actor reference — a DID
// or a domain-form handle — before we spend a round-trip resolving it. Guards the
// profile routes so garbage path segments 404 without hitting the API.
func isValidActor(s string) bool {
	if s == "" {
		return false
	}
	if _, err := syntax.ParseDID(s); err == nil {
		return true
	}
	if _, err := syntax.ParseHandle(s); err == nil {
		return true
	}
	return false
}

// profileFeedSentinelURL builds the infinite-scroll sentinel URL for an actor's feed.
// The actor is path-escaped; a handle contains dots (not slashes) so it rides the path
// segment cleanly, but escaping keeps any exotic DID characters safe.
func profileFeedSentinelURL(actor, nextCursor string) string {
	if nextCursor == "" {
		return ""
	}
	return "/profile/" + url.PathEscape(actor) + "/feed?cursor=" + url.QueryEscape(nextCursor)
}

// ProfilePageData is the data envelope for the profile view page.
type ProfilePageData struct {
	LayoutData
	Profile *feed.Profile
	Actor   string // the actor param, used by the inline edit form's fetch target
}

// HandleProfilePage renders an actor's profile: the getProfile header followed by the
// author feed with the shared infinite-scroll pattern. Route: GET /profile/:actor.
// A malformed actor or a resolution failure renders the 404 template.
func (h *UIHandler) HandleProfilePage(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	actor := c.Param("actor")

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in profile handler", "did", did, "err", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	if !isValidActor(actor) {
		h.Handle404(c)
		return
	}

	profile, err := h.feedClient.GetProfile(c.Request.Context(), did, sessionID, actor)
	if err != nil {
		// Distinguish a dead session (send to login) from a genuinely unresolvable actor
		// (404) — a dead session would otherwise masquerade as "profile not found".
		if h.sessionDead(did, sessionID, err) {
			c.Redirect(http.StatusFound, "/login")
			return
		}
		slog.Info("GetProfile resolution failed", "actor", actor, "did", did, "err", err)
		h.Handle404(c)
		return
	}

	// Build the chrome first so its single getPreferences call also supplies the muted
	// words used to filter the author feed (no second getPreferences on this render).
	data := ProfilePageData{
		LayoutData: h.buildLayoutBase(c, did, sessionID, user),
		Profile:    profile,
		Actor:      actor,
	}

	// Author feed is non-fatal — a header with an empty feed still renders.
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if fetched, err := h.feedClient.GetAuthorFeed(
		c.Request.Context(), did, sessionID, actor, "", uiFeedLimit, data.MutedWords,
	); err != nil {
		slog.Error("GetAuthorFeed in profile handler", "actor", actor, "did", did, "err", err)
	} else {
		feedPage = fetched
	}

	data.FeedPage = feedPage
	data.FeedType = "profile"
	data.SentinelURL = profileFeedSentinelURL(actor, feedPage.NextCursor)

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplProfile.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render profile page", "err", err)
	}
}

// HandleProfileFeedPartial serves the HTMX "feed-more" fragment for an actor's feed.
// Route: GET /profile/:actor/feed?cursor= — only ever hit by the scroll sentinel, which
// always carries a cursor, so it returns the pagination fragment.
func (h *UIHandler) HandleProfileFeedPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	actor := c.Param("actor")
	cursor := c.Query("cursor")

	if !isValidActor(actor) {
		c.Status(http.StatusBadRequest)
		return
	}

	mutedWords := h.mutedWordsFor(c.Request.Context(), did, sessionID)
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if fetched, err := h.feedClient.GetAuthorFeed(
		c.Request.Context(), did, sessionID, actor, cursor, uiFeedLimit, mutedWords,
	); err != nil {
		slog.Error("GetAuthorFeed (partial)", "actor", actor, "did", did, "err", err)
	} else {
		feedPage = fetched
	}

	data := LayoutData{
		FeedPage:    feedPage,
		FeedType:    "profile",
		SentinelURL: profileFeedSentinelURL(actor, feedPage.NextCursor),
	}

	tmplName := "feed"
	if cursor != "" {
		tmplName = "feed-more"
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplProfile.ExecuteTemplate(c.Writer, tmplName, data); err != nil {
		slog.Error("render profile feed partial", "template", tmplName, "err", err)
	}
}

// ProfileHandler holds dependencies for the profile JSON API and the profile-edit
// mutation. The JSON GETs are the iOS surface; the PUT drives the web edit form too.
type ProfileHandler struct {
	queries *db.Queries
	client  *feed.Client
}

// NewProfileHandler constructs a ProfileHandler.
func NewProfileHandler(queries *db.Queries, client *feed.Client) *ProfileHandler {
	return &ProfileHandler{queries: queries, client: client}
}

// HandleGetProfile returns an actor's profile as JSON. Route: GET /api/profile/:actor.
func (h *ProfileHandler) HandleGetProfile(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	actor := c.Param("actor")

	if !isValidActor(actor) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid actor"})
		return
	}

	profile, err := h.client.GetProfile(c.Request.Context(), did, sessionID, actor)
	if err != nil {
		if respondDeadSessionJSON(c, h.client, did, sessionID, err) {
			return
		}
		slog.Info("GetProfile (JSON) failed", "actor", actor, "did", did, "err", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	c.JSON(http.StatusOK, profile)
}

// HandleGetProfileFeed returns a page of an actor's posts as JSON.
// Route: GET /api/profile/:actor/feed?cursor=&limit=.
func (h *ProfileHandler) HandleGetProfileFeed(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	actor := c.Param("actor")
	cursor := c.Query("cursor")
	limit := parseLimit(c)

	if !isValidActor(actor) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid actor"})
		return
	}

	mutedWords := fetchMutedWords(c.Request.Context(), h.client, did, sessionID)
	page, err := h.client.GetAuthorFeed(c.Request.Context(), did, sessionID, actor, cursor, limit, mutedWords)
	if err != nil {
		if respondDeadSessionJSON(c, h.client, did, sessionID, err) {
			return
		}
		slog.Error("GetAuthorFeed (JSON) failed", "actor", actor, "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch author feed"})
		return
	}

	c.JSON(http.StatusOK, page)
}

// typeaheadLimit caps searchActorsTypeahead results for the composer @-mention dropdown.
const typeaheadLimit = 8

// HandleActorTypeahead returns lean actor suggestions for the composer's @-mention
// dropdown. Route: GET /api/actors/typeahead?q=. A blank q returns an empty array with
// 200 (not an error), so the client can call it freely without special-casing the
// no-query state. Mounted under the operations rate limiter (60/min per DID).
func (h *ProfileHandler) HandleActorTypeahead(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	q := strings.TrimSpace(c.Query("q"))

	suggestions, err := h.client.SearchActorsTypeahead(c.Request.Context(), did, sessionID, q, typeaheadLimit)
	if err != nil {
		slog.Error("SearchActorsTypeahead failed", "q", q, "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "typeahead failed"})
		return
	}

	c.JSON(http.StatusOK, suggestions)
}

// HandleUpdateProfile edits the authenticated user's own display name and bio via a
// get-then-put that preserves avatar/banner (see feed.UpdateProfile). Route:
// PUT /api/profile. HTMX/JS submits form-encoded (Gotcha 2), so fields are read with
// c.PostForm; the response is JSON so the web form and iOS share one contract.
func (h *ProfileHandler) HandleUpdateProfile(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	displayName := strings.TrimSpace(c.PostForm("display_name"))
	description := strings.TrimSpace(c.PostForm("description"))

	if utf8.RuneCountInString(displayName) > profileDisplayNameLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "display name is too long"})
		return
	}
	if utf8.RuneCountInString(description) > profileDescriptionLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bio is too long"})
		return
	}

	profile, err := h.client.UpdateProfile(c.Request.Context(), did, sessionID, displayName, description)
	if err != nil {
		slog.Error("UpdateProfile failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to update profile"})
		return
	}

	// Local cache note: the users row caches handle + avatar only. This edit touches
	// neither (display name and bio are not cached, avatar/banner are preserved
	// untouched), so there is nothing to write back to the users table here.
	_ = h.queries

	c.JSON(http.StatusOK, profile)
}
