package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

const uiFeedLimit = 20

var uiHashtagRe = regexp.MustCompile(`#[^\s#<>&"']+`)

// atURIRe matches valid AT Protocol URIs (at://did/collection/rkey).
// Used by safeAtURI to gate what we mark as template.URL.
var atURIRe = regexp.MustCompile(`^at://[a-zA-Z0-9:._/\-]+$`)

// safeAtURI validates that uri is a well-formed AT URI (or empty string,
// used for top-level posts where ReplyRootURI is unset) and returns it
// as template.URL so html/template does not apply URL-scheme filtering.
// Go's template engine classifies data-*uri* attributes as URL context and
// rejects at:// (not an approved scheme) with #ZgotmplZ; this function is
// the narrow bypass, applied only after format validation.
func safeAtURI(uri string) template.URL {
	if uri == "" || atURIRe.MatchString(uri) {
		return template.URL(uri)
	}
	return template.URL("")
}

// PageUser is the user representation passed to all page templates.
type PageUser struct {
	DID    string
	Handle string
	Plan   string
	Avatar string
}

// LayoutData is the data envelope passed to every page rendered with layout.html.
type LayoutData struct {
	User        PageUser
	Theme       string
	RecentTags  []string
	SavedFeeds  []feed.SavedFeed // pinned feeds for the tab bar; nil = no tabs
	// Feed state
	FeedPage      *feed.FeedPage
	FeedType      string   // "following" | "hashtag" | "custom"
	FeedTags      []string // active hashtag tags (for display + next-page URL)
	FeedCustomURI string   // AT URI of the active custom feed (FeedType=="custom")
	SentinelURL   string   // next-page URL embedded in the scroll sentinel
}

// TemplatesPageData is the data envelope for the templates management page.
type TemplatesPageData struct {
	LayoutData
	Templates []templateResponse
}

// ThreadPageData is the data envelope for the thread view page.
type ThreadPageData struct {
	LayoutData
	Thread *feed.ThreadView
	Error  string
}

// UIHandler renders HTML pages.
type UIHandler struct {
	queries       *db.Queries
	secret        []byte
	feedClient    *feed.Client
	tmplHome      *template.Template
	tmplTemplates *template.Template
	tmplThread    *template.Template
	tmplLogin     *template.Template
	tmpl404       *template.Template
	tmpl500       *template.Template
}

// NewUIHandler parses all page templates and returns a ready UIHandler.
// Returns an error if any template file cannot be parsed — callers should
// treat this as fatal at startup.
func NewUIHandler(queries *db.Queries, secret []byte, feedClient *feed.Client) (*UIHandler, error) {
	funcMap := template.FuncMap{
		// safeAtURI validates and marks an AT Protocol URI safe for URL-context attributes.
		"safeAtURI": safeAtURI,
		// urlquote percent-encodes a string for use in URL query parameters.
		"urlquote": url.QueryEscape,
		// highlightHashtags wraps hashtag tokens in a styled span.
		// Runs the regex on plain text so that apostrophes and other punctuation
		// are detected correctly, then escapes each segment exactly once before
		// assembling the result. Returning template.HTML tells html/template not
		// to apply a second round of escaping.
		// highlightHashtags wraps hashtag tokens in a styled, clickable span.
		// Runs the regex on plain text so that apostrophes and other punctuation
		// are detected correctly, then escapes each segment exactly once before
		// assembling the result. Each span gets an onclick that stops propagation
		// (so the card-level navigateToThread never fires) and switches the feed
		// via switchToHashtagFeed. JSEscapeString guards the tag name embedded
		// inside the JS string literal; HTMLEscapeString handles display text.
		// Returning template.HTML tells html/template not to double-escape.
		"highlightHashtags": func(text string) template.HTML {
			var buf strings.Builder
			last := 0
			for _, m := range uiHashtagRe.FindAllStringIndex(text, -1) {
				buf.WriteString(template.HTMLEscapeString(text[last:m[0]]))
				tag := template.JSEscapeString(text[m[0]+1 : m[1]]) // strip '#', JS-escape
				buf.WriteString(`<span class="post-hashtag" onclick="event.stopPropagation();switchToHashtagFeed(['`)
				buf.WriteString(tag)
				buf.WriteString(`'])">`)
				buf.WriteString(template.HTMLEscapeString(text[m[0]:m[1]]))
				buf.WriteString(`</span>`)
				last = m[1]
			}
			buf.WriteString(template.HTMLEscapeString(text[last:]))
			return template.HTML(buf.String())
		},
		// relativeTime converts an RFC3339 timestamp to a human-readable age.
		"relativeTime": func(indexedAt string) string {
			t, err := time.Parse(time.RFC3339, indexedAt)
			if err != nil {
				return indexedAt
			}
			dur := time.Since(t)
			switch {
			case dur < time.Minute:
				return "just now"
			case dur < time.Hour:
				return fmt.Sprintf("%dm", int(dur.Minutes()))
			case dur < 24*time.Hour:
				return fmt.Sprintf("%dh", int(dur.Hours()))
			default:
				return fmt.Sprintf("%dd", int(dur.Hours()/24))
			}
		},
		// initials returns the first letter of the display name or handle.
		"initials": func(displayName, handle string) string {
			for _, s := range []string{displayName, handle} {
				r := []rune(s)
				if len(r) > 0 {
					return strings.ToUpper(string(r[0:1]))
				}
			}
			return "?"
		},
		// formatCount formats a large number with a K suffix.
		"formatCount": func(n int64) string {
			if n >= 1000 {
				return fmt.Sprintf("%.1fK", float64(n)/1000)
			}
			return strconv.FormatInt(n, 10)
		},
		// derefStr safely dereferences a *string, returning "" for nil.
		"derefStr": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
	}

	tmplHome, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/post_card.html",
		"templates/partials/feed.html",
		"templates/partials/feed_controls.html",
		"templates/index.html",
	)
	if err != nil {
		return nil, err
	}
	tmplTemplates, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/template_row.html",
		"templates/templates.html",
	)
	if err != nil {
		return nil, err
	}
	tmplThread, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/post_card.html",
		"templates/thread.html",
	)
	if err != nil {
		return nil, err
	}
	tmplLogin, err := template.ParseFiles("templates/login.html")
	if err != nil {
		return nil, err
	}
	tmpl404, err := template.ParseFiles("templates/404.html")
	if err != nil {
		return nil, err
	}
	tmpl500, err := template.ParseFiles("templates/500.html")
	if err != nil {
		return nil, err
	}
	return &UIHandler{
		queries:       queries,
		secret:        secret,
		feedClient:    feedClient,
		tmplHome:      tmplHome,
		tmplTemplates: tmplTemplates,
		tmplThread:    tmplThread,
		tmplLogin:     tmplLogin,
		tmpl404:       tmpl404,
		tmpl500:       tmpl500,
	}, nil
}

// Handle404 renders the 404 error page.
func (h *UIHandler) Handle404(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusNotFound)
	if err := h.tmpl404.ExecuteTemplate(c.Writer, "404", nil); err != nil {
		slog.Error("render 404 template", "err", err)
	}
}

// Handle500 renders the 500 error page.
func (h *UIHandler) Handle500(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusInternalServerError)
	if err := h.tmpl500.ExecuteTemplate(c.Writer, "500", nil); err != nil {
		slog.Error("render 500 template", "err", err)
	}
}

// HandleLoginPage renders the sign-in page.
// If the request already carries a valid session cookie, redirects to /.
func (h *UIHandler) HandleLoginPage(c *gin.Context) {
	if cookie, err := c.Request.Cookie(auth.SessionCookieName); err == nil {
		if _, _, err := auth.ParseSessionCookie(cookie.Value, h.secret); err == nil {
			c.Redirect(http.StatusFound, "/")
			return
		}
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplLogin.ExecuteTemplate(c.Writer, "login", nil); err != nil {
		slog.Error("render login template", "err", err)
	}
}

// HandleHome renders the full three-column layout with the Following feed pre-loaded.
// RequireSession middleware ensures only authenticated users reach this handler.
func (h *UIHandler) HandleHome(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in home handler", "did", did, "err", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	// Fetch first page of Following feed — non-fatal if unavailable.
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if fetchedPage, err := h.feedClient.GetFollowingFeed(
		c.Request.Context(), did, sessionID, "", uiFeedLimit,
	); err != nil {
		slog.Error("GetFollowingFeed in home handler", "did", did, "err", err)
	} else {
		feedPage = fetchedPage
	}

	data := h.buildLayoutBase(c, did, sessionID, user)
	data.FeedPage = feedPage
	data.FeedType = "following"
	data.SentinelURL = followingSentinelURL(feedPage.NextCursor)

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render home template", "err", err)
	}
}

// HandleFollowingFeedPartial serves HTMX partial responses for the Following feed.
// Without a cursor it returns the full "feed" block (controls + list).
// With a cursor it returns just the "feed-more" fragment for sentinel-based pagination.
func (h *UIHandler) HandleFollowingFeedPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")

	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if fetchedPage, err := h.feedClient.GetFollowingFeed(
		c.Request.Context(), did, sessionID, cursor, uiFeedLimit,
	); err != nil {
		slog.Error("GetFollowingFeed (partial)", "did", did, "err", err)
	} else {
		feedPage = fetchedPage
	}

	data := LayoutData{
		FeedPage:    feedPage,
		FeedType:    "following",
		SentinelURL: followingSentinelURL(feedPage.NextCursor),
	}

	tmplName := "feed"
	if cursor != "" {
		tmplName = "feed-more"
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, tmplName, data); err != nil {
		slog.Error("render following feed partial", "template", tmplName, "err", err)
	}
}

// HandleHashtagFeedPartial serves HTMX partial responses for the merged hashtag feed.
// Without a cursor it returns the full "feed" block.
// With a cursor it returns the "feed-more" fragment for pagination.
func (h *UIHandler) HandleHashtagFeedPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")
	rawTags := c.Query("tags")

	var tags []string
	for _, t := range strings.Split(rawTags, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}

	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if len(tags) > 0 {
		if fetchedPage, err := h.feedClient.GetHashtagFeed(
			c.Request.Context(), did, sessionID, tags, cursor, uiFeedLimit,
		); err != nil {
			slog.Error("GetHashtagFeed (partial)", "did", did, "tags", tags, "err", err)
		} else {
			feedPage = fetchedPage
		}
	}

	data := LayoutData{
		FeedPage:    feedPage,
		FeedType:    "hashtag",
		FeedTags:    tags,
		SentinelURL: hashtagSentinelURL(tags, feedPage.NextCursor),
	}

	tmplName := "feed"
	if cursor != "" {
		tmplName = "feed-more"
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, tmplName, data); err != nil {
		slog.Error("render hashtag feed partial", "template", tmplName, "err", err)
	}
}

// HandleCustomFeedPartial serves HTMX partial responses for a Bluesky algorithm feed.
// Without a cursor it returns the full "feed" block; with a cursor it returns "feed-more".
func (h *UIHandler) HandleCustomFeedPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")
	feedURI := c.Query("uri")

	if !atURIRe.MatchString(feedURI) {
		c.Status(http.StatusBadRequest)
		return
	}

	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	if fetchedPage, err := h.feedClient.GetCustomFeed(
		c.Request.Context(), did, sessionID, feedURI, cursor, uiFeedLimit,
	); err != nil {
		slog.Error("GetCustomFeed (partial)", "did", did, "uri", feedURI, "err", err)
	} else {
		feedPage = fetchedPage
	}

	data := LayoutData{
		FeedPage:      feedPage,
		FeedType:      "custom",
		FeedCustomURI: feedURI,
		SentinelURL:   customFeedSentinelURL(feedURI, feedPage.NextCursor),
	}

	tmplName := "feed"
	if cursor != "" {
		tmplName = "feed-more"
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, tmplName, data); err != nil {
		slog.Error("render custom feed partial", "template", tmplName, "err", err)
	}
}

// resolveUserForTemplates looks up the user row by DID and writes an error
// response if it fails. Used by the template web handlers.
func (h *UIHandler) resolveUserForTemplates(c *gin.Context, did string) (db.User, bool) {
	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		} else {
			slog.Error("GetUserByDID", "did", did, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return db.User{}, false
	}
	return user, true
}

// buildLayoutBase populates the common LayoutData fields for a given user,
// including fetching saved feeds for the tab bar (non-fatal on failure).
func (h *UIHandler) buildLayoutBase(c *gin.Context, did, sessionID string, user db.User) LayoutData {
	tagRows, err := h.queries.GetRecentTagsByUser(
		c.Request.Context(),
		pgtype.Int4{Int32: user.ID, Valid: true},
	)
	if err != nil {
		slog.Error("GetRecentTagsByUser", "user_id", user.ID, "err", err)
	}
	recentTags := make([]string, 0, len(tagRows))
	for _, row := range tagRows {
		recentTags = append(recentTags, row.Tag)
	}

	theme := user.Theme
	if user.Plan == "free" && theme != "ocean" {
		theme = "ocean"
	}

	handle := user.Handle.String
	if !user.Handle.Valid || handle == "" {
		handle = did
	}

	avatar := ""
	if user.Avatar.Valid {
		avatar = user.Avatar.String
	}

	// Fetch saved feeds for the tab bar — degrade to Following-only on failure.
	savedFeeds, err := h.feedClient.GetSavedFeeds(c.Request.Context(), did, sessionID)
	if err != nil {
		slog.Warn("GetSavedFeeds failed, using following-only tab bar", "did", did, "err", err)
		savedFeeds = []feed.SavedFeed{{DisplayName: "Following", IsTimeline: true}}
	}

	return LayoutData{
		User: PageUser{
			DID:    did,
			Handle: handle,
			Plan:   user.Plan,
			Avatar: avatar,
		},
		Theme:      theme,
		RecentTags: recentTags,
		SavedFeeds: savedFeeds,
	}
}

// HandleTemplatesPage renders the template management page.
func (h *UIHandler) HandleTemplatesPage(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, ok := h.resolveUserForTemplates(c, did)
	if !ok {
		return
	}

	rows, err := h.queries.ListTemplatesByUser(
		c.Request.Context(),
		pgtype.Int4{Int32: user.ID, Valid: true},
	)
	if err != nil {
		slog.Error("ListTemplatesByUser in templates page", "user_id", user.ID, "err", err)
		rows = nil
	}

	templates := make([]templateResponse, len(rows))
	for i, t := range rows {
		templates[i] = toResponse(t)
	}

	data := TemplatesPageData{
		LayoutData: h.buildLayoutBase(c, did, sessionID, user),
		Templates:  templates,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplTemplates.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render templates page", "err", err)
	}
}

// HandleWebCreateTemplate handles the HTMX form POST from the templates page.
// Accepts application/x-www-form-urlencoded, returns the new template-row HTML
// on success or JSON error on failure (so JS can read the error message).
func (h *UIHandler) HandleWebCreateTemplate(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	name := strings.TrimSpace(c.PostForm("name"))
	suffix := strings.TrimSpace(c.PostForm("suffix"))
	if name == "" || suffix == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and suffix are required"})
		return
	}

	user, ok := h.resolveUserForTemplates(c, did)
	if !ok {
		return
	}

	t, err := h.queries.CreateTemplate(c.Request.Context(), db.CreateTemplateParams{
		UserID:   pgtype.Int4{Int32: user.ID, Valid: true},
		Name:     name,
		Suffix:   suffix,
		Position: pgtype.Int4{Int32: 0, Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a template with that name already exists"})
			return
		}
		slog.Error("CreateTemplate (web)", "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create template"})
		return
	}

	r := toResponse(t)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusCreated)
	if err := h.tmplTemplates.ExecuteTemplate(c.Writer, "template-row", r); err != nil {
		slog.Error("render template-row (create)", "err", err)
	}
}

// HandleWebUpdateTemplate handles the HTMX form PUT from the template edit form.
// Accepts application/x-www-form-urlencoded, returns the updated template-row HTML
// on success or JSON error on failure.
func (h *UIHandler) HandleWebUpdateTemplate(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	idStr := c.Param("id")
	id64, err := strconv.ParseInt(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template id"})
		return
	}
	id := int32(id64)

	name := strings.TrimSpace(c.PostForm("name"))
	suffix := strings.TrimSpace(c.PostForm("suffix"))
	if name == "" || suffix == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and suffix are required"})
		return
	}

	user, ok := h.resolveUserForTemplates(c, did)
	if !ok {
		return
	}

	t, err := h.queries.UpdateTemplate(c.Request.Context(), db.UpdateTemplateParams{
		ID:     id,
		UserID: pgtype.Int4{Int32: user.ID, Valid: true},
		Name:   name,
		Suffix: suffix,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
			return
		}
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a template with that name already exists"})
			return
		}
		slog.Error("UpdateTemplate (web)", "id", id, "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update template"})
		return
	}

	r := toResponse(t)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplTemplates.ExecuteTemplate(c.Writer, "template-row", r); err != nil {
		slog.Error("render template-row (update)", "err", err)
	}
}

// HandleThreadPage renders the full thread view for the post at the given AT URI.
// Route: GET /thread?uri=<at-uri>
func (h *UIHandler) HandleThreadPage(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in thread handler", "did", did, "err", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	data := ThreadPageData{
		LayoutData: h.buildLayoutBase(c, did, sessionID, user),
	}

	uri := c.Query("uri")
	if !atURIRe.MatchString(uri) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Status(http.StatusBadRequest)
		data.Error = "Invalid post URL."
		if err := h.tmplThread.ExecuteTemplate(c.Writer, "layout", data); err != nil {
			slog.Error("render thread page (bad uri)", "err", err)
		}
		return
	}

	threadView, err := h.feedClient.GetThread(c.Request.Context(), did, sessionID, uri)
	if err != nil {
		switch {
		case errors.Is(err, feed.ErrThreadNotFound):
			data.Error = "This post could not be found."
		case errors.Is(err, feed.ErrThreadBlocked):
			data.Error = "This post is from an account you've blocked."
		default:
			slog.Error("GetThread", "uri", uri, "did", did, "err", err)
			data.Error = "Unable to load thread. Please try again."
		}
	} else {
		data.Thread = threadView
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplThread.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render thread page", "err", err)
	}
}

func followingSentinelURL(nextCursor string) string {
	if nextCursor == "" {
		return ""
	}
	return "/feed/following?cursor=" + url.QueryEscape(nextCursor)
}

func hashtagSentinelURL(tags []string, nextCursor string) string {
	if nextCursor == "" || len(tags) == 0 {
		return ""
	}
	return "/feed/hashtags?tags=" + strings.Join(tags, ",") + "&cursor=" + url.QueryEscape(nextCursor)
}

func customFeedSentinelURL(feedURI, nextCursor string) string {
	if nextCursor == "" || feedURI == "" {
		return ""
	}
	return "/feed/custom?uri=" + url.QueryEscape(feedURI) + "&cursor=" + url.QueryEscape(nextCursor)
}
