package handlers

import (
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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

const uiFeedLimit = 20

var uiHashtagRe = regexp.MustCompile(`#[^\s#<>&"']+`)

// PageUser is the user representation passed to all page templates.
type PageUser struct {
	DID    string
	Handle string
	Plan   string
	Avatar string
}

// LayoutData is the data envelope passed to every page rendered with layout.html.
type LayoutData struct {
	User       PageUser
	Theme      string
	RecentTags []string
	// Feed state
	FeedPage    *feed.FeedPage
	FeedType    string   // "following" | "hashtag"
	FeedTags    []string // active hashtag tags (for display + next-page URL)
	SentinelURL string   // next-page URL embedded in the scroll sentinel
}

// UIHandler renders HTML pages.
type UIHandler struct {
	queries    *db.Queries
	secret     []byte
	feedClient *feed.Client
	tmplHome   *template.Template
	tmplLogin  *template.Template
}

// NewUIHandler parses all page templates and returns a ready UIHandler.
// Returns an error if any template file cannot be parsed — callers should
// treat this as fatal at startup.
func NewUIHandler(queries *db.Queries, secret []byte, feedClient *feed.Client) (*UIHandler, error) {
	funcMap := template.FuncMap{
		// highlightHashtags wraps hashtag tokens in a styled span.
		// The input is HTML-escaped first so user content cannot inject markup.
		"highlightHashtags": func(text string) template.HTML {
			escaped := template.HTMLEscapeString(text)
			result := uiHashtagRe.ReplaceAllStringFunc(escaped, func(m string) string {
				return `<span class="post-hashtag">` + m + `</span>`
			})
			return template.HTML(result)
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
		"templates/partials/feed.html",
		"templates/partials/feed_controls.html",
		"templates/index.html",
	)
	if err != nil {
		return nil, err
	}
	tmplLogin, err := template.ParseFiles("templates/login.html")
	if err != nil {
		return nil, err
	}
	return &UIHandler{
		queries:    queries,
		secret:     secret,
		feedClient: feedClient,
		tmplHome:   tmplHome,
		tmplLogin:  tmplLogin,
	}, nil
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

	// Free users are locked to the ocean theme regardless of what is stored.
	theme := user.Theme
	if user.Plan == "free" && theme != "ocean" {
		theme = "ocean"
	}

	handle := user.Handle.String
	if !user.Handle.Valid || handle == "" {
		handle = did
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

	data := LayoutData{
		User: PageUser{
			DID:    did,
			Handle: handle,
			Plan:   user.Plan,
		},
		Theme:       theme,
		RecentTags:  recentTags,
		FeedPage:    feedPage,
		FeedType:    "following",
		SentinelURL: followingSentinelURL(feedPage.NextCursor),
	}

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
