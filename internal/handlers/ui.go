package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/auth"
	"github.com/rsherman/draftsky/internal/bluesky"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

const uiFeedLimit = 20

// uiFeedFetchTimeout bounds a page-blocking feed/chrome fetch (the lazy home
// partials). The degradation UI makes a fast fail cheap — a skeleton flips to a
// "Bluesky isn't responding" notice with a Retry button — so these fail at ~6s
// rather than hanging, instead of the PDS client's longer default. (createRecord,
// where a post is worth waiting for, keeps its own longer budget — see bluesky.)
const uiFeedFetchTimeout = 6 * time.Second

var uiHashtagRe = regexp.MustCompile(`#[^\s#<>&"']+`)

// uiMentionRe matches @-mentions of domain-form handles for display highlighting.
// It mirrors the outgoing-post mention regex: '@' must begin the token (start of
// string or a whitespace boundary, so emails and mid-word '@' are excluded) and a
// handle is two or more dot-joined [a-zA-Z0-9-] segments. Group 1 is the "@handle"
// token, excluding the leading boundary character. This is a v1 display-only regex
// pass over the post text — it does not consume the post's actual mention facet
// byte ranges (PostView does not carry facets yet).
var uiMentionRe = regexp.MustCompile(`(?:^|[\s])(@[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+)`)

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
	CSRFToken   string // double-submit token surfaced in the layout <meta> tag
	RecentTags  []string
	SavedFeeds  []feed.SavedFeed // pinned feeds for the tab bar; nil = no tabs
	UnreadCount int64            // unread notification count for the left-rail badge; 0 hides it
	// MutedWords is fetched alongside SavedFeeds in the single per-render getPreferences
	// call (buildLayoutBase → GetPreferences). Not rendered; carried so the same handler
	// can pass it to the feed fetch (Following on home, thread posts) without a second
	// getPreferences. Feed-partial handlers, which don't build the full layout, fetch
	// muted words on their own via mutedWordsFor.
	MutedWords []feed.MutedWord
	// LazyChrome is set only by the shell-only home handler. When true, layout.html
	// renders the tab bar and recent-tags rail as empty HTMX lazy regions (hx-get on
	// load) instead of server-rendering them, so GET / responds instantly with no
	// upstream (PDS) calls. Every other page leaves it false and renders the chrome
	// synchronously from SavedFeeds/RecentTags exactly as before.
	LazyChrome bool
	// TabsDegraded is set by the /feed/tabs partial when its getPreferences fetch
	// failed: the tab bar falls back to Following-only and renders a small note.
	TabsDegraded bool

	// Feed state
	FeedPage      *feed.FeedPage
	FeedType      string   // "following" | "hashtag" | "custom"
	FeedTags      []string // active hashtag tags (for display + next-page URL)
	FeedAuthor    string   // when set on a hashtag feed: "#tag by @author" (handle or DID)
	FeedCustomURI string   // AT URI of the active custom feed (FeedType=="custom")
	SentinelURL   string   // next-page URL embedded in the scroll sentinel
	// FeedError marks that the feed's upstream fetch failed (timeout/connection/5xx —
	// as opposed to a successful empty result). The feed partial then renders the
	// "Bluesky isn't responding" notice with a Retry button targeting RetryURL,
	// instead of the misleading "no posts yet" empty state.
	FeedError bool
	RetryURL  string // hx-get URL the failure notice's Retry button re-triggers
	// SessionExpired marks that the upstream fetch failed because the OAuth session is
	// DEAD (invalid_grant on refresh — see auth.IsDeadSession), not merely unreachable.
	// Retrying can never help, so the feed partial renders a distinct "session expired,
	// sign in again" notice linking to /auth/login instead of the FeedError network notice
	// with its Retry button. Takes precedence over FeedError in the template.
	SessionExpired bool
}

// TemplatesPageData is the data envelope for the templates management page.
// CanAddTemplate is false when a free user is at the template cap; the Add form
// then renders disabled with LimitMessage inline. Plan is carried via LayoutData.User.
type TemplatesPageData struct {
	LayoutData
	Templates      []templateResponse
	CanAddTemplate bool
	LimitMessage   string
}

// ThreadPageData is the data envelope for the thread view page.
type ThreadPageData struct {
	LayoutData
	Thread *feed.ThreadView
	Error  string
}

// NotificationsPageData is the data envelope for the notifications page. It reuses
// LayoutData.SentinelURL for the infinite-scroll sentinel.
type NotificationsPageData struct {
	LayoutData
	Notifications []feed.Notification
	Error         string
}

// AdminStatsData is the data envelope for the owner-only admin stats page. It embeds
// LayoutData for the chrome and flattens the two stats query rows into template fields.
type AdminStatsData struct {
	LayoutData
	TotalUsers     int64
	NewToday       int64
	NewThisWeek    int64
	DAU            int64
	WAU            int64
	MAU            int64
	TotalTemplates int64
	TotalPosts     int64
}

// UIHandler renders HTML pages.
type UIHandler struct {
	queries           *db.Queries
	secret            []byte
	feedClient        *feed.Client
	tmplHome          *template.Template
	tmplTemplates     *template.Template
	tmplThread        *template.Template
	tmplProfile       *template.Template
	tmplNotifications *template.Template
	tmplSettings      *template.Template
	tmplAdmin         *template.Template
	tmplLogin         *template.Template
	tmplRoadmap       *template.Template
	tmpl404           *template.Template
	tmpl500           *template.Template
}

// assetURLToFile maps a public /static URL path to the on-disk file whose content
// hash busts its cache. Keep it in sync with the assets templates actually reference.
var assetURLToFile = map[string]string{
	"/static/app.js":            "static/app.js",
	"/static/style.css":         "static/style.css",
	"/static/vendor/hls.min.js": "static/vendor/hls.min.js",
}

// computeAssetVersions hashes each cache-busted static asset once at startup and
// returns URL-path → short-hash. The hash is the first 8 hex of the SHA-256 of the
// file's bytes: it is CONTENT-ADDRESSED, so it changes automatically the moment a
// deploy ships new asset content and stays identical across restarts that don't.
// Appended to the asset URL as ?v=<hash>, it turns each deploy's asset into a
// distinct URL the browser must re-fetch — ending the Gotcha 10 (stale app.js) class
// of bug for END USERS without any hard refresh. Startup cost is three file reads.
// A file that cannot be read yields no entry (the template emits the bare URL with no
// ?v) rather than failing startup — worst case is the pre-existing caching behaviour.
func computeAssetVersions() map[string]string {
	versions := make(map[string]string, len(assetURLToFile))
	for urlPath, file := range assetURLToFile {
		b, err := os.ReadFile(file)
		if err != nil {
			slog.Warn("asset version: read failed, cache-busting disabled for this file", "file", file, "err", err)
			continue
		}
		sum := sha256.Sum256(b)
		versions[urlPath] = hex.EncodeToString(sum[:])[:8]
	}
	return versions
}

// NewUIHandler parses all page templates and returns a ready UIHandler.
// Returns an error if any template file cannot be parsed — callers should
// treat this as fatal at startup.
func NewUIHandler(queries *db.Queries, secret []byte, feedClient *feed.Client) (*UIHandler, error) {
	assetVersions := computeAssetVersions()
	funcMap := template.FuncMap{
		// assetVersion returns the content hash for a /static URL, for use as a
		// ?v= cache-busting query param. Empty string when the asset is unknown or
		// unreadable, so the template simply emits the un-versioned URL.
		"assetVersion": func(urlPath string) string { return assetVersions[urlPath] },
		// safeAtURI validates and marks an AT Protocol URI safe for URL-context attributes.
		"safeAtURI": safeAtURI,
		// urlquote percent-encodes a string for use in URL query parameters.
		"urlquote": url.QueryEscape,
		// highlightFacets wraps hashtag and @-mention tokens in styled spans.
		// Runs both regexes on plain text (so apostrophes and other punctuation are
		// detected correctly), merges the matches in byte order, then escapes each
		// segment exactly once before assembling the result (Gotcha 5 — escaping
		// first would corrupt offsets and double-escape). Returning template.HTML
		// tells html/template not to apply a second round of escaping.
		//
		// Hashtag spans get an onclick that stops propagation (so the card-level
		// navigateToThread never fires) and opens the hashtag context menu via
		// openHashtagMenu(event, '<tag>', '<authorHandle>'). The menu offers
		// "See #tag posts" (the ordinary merged hashtag feed) and, when authorHandle
		// is non-empty, "See #tag posts by @author" (searchPosts author filter). The
		// authorHandle is the handle of the post the hashtag belongs to — threaded in
		// as a second argument. It is empty in author-less contexts (e.g. profile
		// bios), where the menu shows a single option. JSEscapeString guards both the
		// tag and the handle inside their JS string literals. Mention spans work the
		// same way: onclick stops propagation and calls navigateToProfile(event,
		// '<handle>'), sending the user to /profile/<handle>. The mention handle
		// (token minus the leading '@') goes through JSEscapeString for the JS string
		// literal — it is already regex-validated domain-form text from uiMentionRe,
		// but is escaped anyway as defence in depth (never trust display text). A
		// handle that no longer resolves simply 404s at /profile, which is acceptable.
		"highlightFacets": func(text, authorHandle string) template.HTML {
			jsAuthor := template.JSEscapeString(authorHandle)
			const (
				kindHashtag = iota
				kindMention
				kindLink
			)
			type span struct {
				start, end int
				kind       int
				href       string // links only; the validated http(s) URI
			}
			var spans []span

			// Links are detected first (no exclusions) so a URL wins over an
			// unanchored hashtag match inside it — the "#anchor" in
			// "https://example.com/#anchor" stays part of the link, not a tag.
			// Real hashtags/mentions never overlap a link (their '#'/'@' prefix
			// is not a URL boundary char — see urlRe), so they are only added
			// when clear of every detected link range.
			links := bluesky.DetectLinks(text, nil)
			for _, lm := range links {
				spans = append(spans, span{start: lm.Start, end: lm.End, kind: kindLink, href: lm.URI})
			}
			addIfClear := func(start, end, kind int) {
				for _, lm := range links {
					if start < lm.End && lm.Start < end {
						return
					}
				}
				spans = append(spans, span{start: start, end: end, kind: kind})
			}
			for _, m := range uiHashtagRe.FindAllStringIndex(text, -1) {
				addIfClear(m[0], m[1], kindHashtag)
			}
			for _, m := range uiMentionRe.FindAllStringSubmatchIndex(text, -1) {
				// m[2]/m[3] bound capture group 1 — the "@handle", excluding the
				// leading boundary whitespace.
				addIfClear(m[2], m[3], kindMention)
			}
			sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

			var buf strings.Builder
			last := 0
			for _, s := range spans {
				if s.start < last {
					continue // defensive: skip any overlap
				}
				buf.WriteString(template.HTMLEscapeString(text[last:s.start]))
				token := text[s.start:s.end]
				switch s.kind {
				case kindMention:
					handle := template.JSEscapeString(token[1:]) // strip '@', JS-escape
					buf.WriteString(`<span class="post-mention" onclick="event.stopPropagation();navigateToProfile(event,'`)
					buf.WriteString(handle)
					buf.WriteString(`')">`)
					buf.WriteString(template.HTMLEscapeString(token))
					buf.WriteString(`</span>`)
				case kindLink:
					// href is a detection-validated http(s) URI, never raw text into an
					// attribute; HTMLEscapeString guards the attribute and visible-text
					// contexts (Gotcha 1's spirit: validate, then mark safe).
					buf.WriteString(`<a class="post-link" href="`)
					buf.WriteString(template.HTMLEscapeString(s.href))
					buf.WriteString(`" target="_blank" rel="noopener noreferrer nofollow" onclick="event.stopPropagation()">`)
					buf.WriteString(template.HTMLEscapeString(token))
					buf.WriteString(`</a>`)
				default: // kindHashtag
					tag := template.JSEscapeString(token[1:]) // strip '#', JS-escape
					buf.WriteString(`<span class="post-hashtag" onclick="event.stopPropagation();openHashtagMenu(event,'`)
					buf.WriteString(tag)
					buf.WriteString(`','`)
					buf.WriteString(jsAuthor)
					buf.WriteString(`')">`)
					buf.WriteString(template.HTMLEscapeString(token))
					buf.WriteString(`</span>`)
				}
				last = s.end
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
		// httpsURL returns u only when it is a well-formed https:// URL, else "".
		// Used to gate video playlist URLs before they reach a data attribute: a
		// non-https (or malformed) playlist is dropped so the card renders a static
		// thumbnail with no playback wiring rather than an unusable/mixed-content src.
		"httpsURL": func(u string) string {
			if p, err := url.Parse(u); err == nil && p.Scheme == "https" && p.Host != "" {
				return u
			}
			return ""
		},
	}

	tmplHome, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
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
		"templates/partials/chrome.html",
		"templates/partials/template_row.html",
		"templates/templates.html",
	)
	if err != nil {
		return nil, err
	}
	tmplThread, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
		"templates/partials/post_card.html",
		"templates/thread.html",
	)
	if err != nil {
		return nil, err
	}
	tmplProfile, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
		"templates/partials/post_card.html",
		"templates/partials/feed.html",
		"templates/partials/feed_controls.html",
		"templates/profile.html",
	)
	if err != nil {
		return nil, err
	}
	tmplNotifications, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
		"templates/partials/notification_row.html",
		"templates/notifications.html",
	)
	if err != nil {
		return nil, err
	}
	tmplSettings, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
		"templates/settings.html",
	)
	if err != nil {
		return nil, err
	}
	tmplAdmin, err := template.New("").Funcs(funcMap).ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
		"templates/partials/chrome.html",
		"templates/admin.html",
	)
	if err != nil {
		return nil, err
	}
	tmplLogin, err := template.New("").Funcs(funcMap).ParseFiles("templates/login.html")
	if err != nil {
		return nil, err
	}
	tmplRoadmap, err := template.New("").Funcs(funcMap).ParseFiles("templates/roadmap.html")
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
		queries:           queries,
		secret:            secret,
		feedClient:        feedClient,
		tmplHome:          tmplHome,
		tmplTemplates:     tmplTemplates,
		tmplThread:        tmplThread,
		tmplProfile:       tmplProfile,
		tmplNotifications: tmplNotifications,
		tmplSettings:      tmplSettings,
		tmplAdmin:         tmplAdmin,
		tmplLogin:         tmplLogin,
		tmplRoadmap:       tmplRoadmap,
		tmpl404:           tmpl404,
		tmpl500:           tmpl500,
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

// HandleRoadmapPage renders the public, hand-curated roadmap marketing page.
// It is intentionally accessible to logged-out visitors (the target audience)
// and logged-in users alike — no session required, no redirect.
func (h *UIHandler) HandleRoadmapPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplRoadmap.ExecuteTemplate(c.Writer, "roadmap", nil); err != nil {
		slog.Error("render roadmap template", "err", err)
	}
}

// HandleHome renders the full three-column layout with the Following feed pre-loaded.
// RequireSession middleware ensures only authenticated users reach this handler.
// HandleHome renders the home page shell INSTANTLY, making no upstream (Bluesky/PDS)
// calls of its own. Its only I/O is the local GetUserByDID lookup. The three
// upstream-dependent regions — the saved-feeds tab bar, the Following feed, and the
// recent-tags rail — are each rendered as an empty HTMX lazy region that fetches its
// own content on load (/feed/tabs, /feed/following, /feed/recent-tags). The unread
// badge starts hidden and is filled by the client's immediate first poll.
//
// This is the fix for the 2026-07-15 incident: when the PDS was slow (TLS handshake
// timeouts), the old synchronous home handler (buildLayoutBase's getPreferences +
// getUnreadCount, plus the Following fetch) made the page take 17-20s to first byte.
// The shell now paints immediately and each region shows a skeleton, then either its
// content or a "Bluesky isn't responding" notice — the degradation has a face.
func (h *UIHandler) HandleHome(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in home handler", "did", did, "err", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	data := buildShellLayout(did, sessionID, user, h.secret)
	data.FeedType = "following"

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render home template", "err", err)
	}
}

// buildShellLayout builds the LayoutData for the instant home shell. It is a PURE
// function of local inputs — it takes no feed client and performs no I/O — so it is
// structurally impossible for it to make an upstream call. That is the enforcement
// behind "GET / makes zero upstream calls": there is no client to reach the PDS with.
// The tab bar and recent-tags rail are deferred to their own lazy partials via
// LazyChrome; the unread badge starts at 0 and is filled by the client poll.
func buildShellLayout(did, sessionID string, user db.User, secret []byte) LayoutData {
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

	return LayoutData{
		User: PageUser{
			DID:    did,
			Handle: handle,
			Plan:   user.Plan,
			Avatar: avatar,
		},
		Theme:      theme,
		CSRFToken:  auth.CSRFToken(sessionID, secret),
		LazyChrome: true,
	}
}

// sessionDead reports whether an upstream fetch failed because the OAuth session is DEAD
// (invalid_grant on refresh — auth.IsDeadSession), and if so invalidates the session's
// cached + persisted state so the half-dead cookie/token mismatch is cleared. Callers that
// render a lazy region set data.SessionExpired on true (the expired notice with a login
// link); synchronous full-page handlers redirect to /login instead. On false the error is
// transient and handled as before (the "Bluesky isn't responding" notice / page error).
func (h *UIHandler) sessionDead(did, sessionID string, err error) bool {
	if !auth.IsDeadSession(err) {
		return false
	}
	slog.Warn("dead OAuth session; invalidating", "did", did, "err", err)
	h.feedClient.InvalidateSession(did, sessionID)
	return true
}

// HandleFollowingFeedPartial serves HTMX partial responses for the Following feed.
// Without a cursor it returns the full "feed" block (controls + list).
// With a cursor it returns just the "feed-more" fragment for sentinel-based pagination.
func (h *UIHandler) HandleFollowingFeedPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")

	ctx, cancel := context.WithTimeout(c.Request.Context(), uiFeedFetchTimeout)
	defer cancel()

	mutedWords := h.mutedWordsFor(ctx, did, sessionID)
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	feedErr, sessionExpired := false, false
	if fetchedPage, err := h.feedClient.GetFollowingFeed(
		ctx, did, sessionID, cursor, uiFeedLimit, mutedWords,
	); err != nil {
		if h.sessionDead(did, sessionID, err) {
			sessionExpired = true
		} else {
			slog.Error("GetFollowingFeed (partial)", "did", did, "err", err)
			feedErr = true
		}
	} else {
		feedPage = fetchedPage
	}

	data := LayoutData{
		FeedPage:    feedPage,
		FeedType:    "following",
		SentinelURL: followingSentinelURL(feedPage.NextCursor),
	}
	// Only surface a notice on the full-feed load (no cursor). A pagination (feed-more)
	// failure just stops the infinite scroll — no notice mid-list. A dead session takes
	// precedence over a transient error.
	if cursor == "" {
		if sessionExpired {
			data.SessionExpired = true
		} else if feedErr {
			data.FeedError = true
			data.RetryURL = c.Request.URL.RequestURI()
		}
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

	// author is optional (the "See #tag posts by @handle" menu option). Reject a
	// malformed author outright rather than silently ignoring it — same guard as the
	// profile routes.
	author := strings.TrimSpace(c.Query("author"))
	if author != "" && !isValidActor(author) {
		c.Status(http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), uiFeedFetchTimeout)
	defer cancel()

	mutedWords := h.mutedWordsFor(ctx, did, sessionID)
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	feedErr, sessionExpired := false, false
	if len(tags) > 0 {
		if fetchedPage, err := h.feedClient.GetHashtagFeed(
			ctx, did, sessionID, tags, author, cursor, uiFeedLimit, mutedWords,
		); err != nil {
			if h.sessionDead(did, sessionID, err) {
				sessionExpired = true
			} else {
				slog.Error("GetHashtagFeed (partial)", "did", did, "tags", tags, "author", author, "err", err)
				feedErr = true
			}
		} else {
			feedPage = fetchedPage
		}
	}

	data := LayoutData{
		FeedPage:    feedPage,
		FeedType:    "hashtag",
		FeedTags:    tags,
		FeedAuthor:  author,
		SentinelURL: hashtagSentinelURL(tags, author, feedPage.NextCursor),
	}
	if cursor == "" {
		if sessionExpired {
			data.SessionExpired = true
		} else if feedErr {
			data.FeedError = true
			data.RetryURL = c.Request.URL.RequestURI()
		}
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

	ctx, cancel := context.WithTimeout(c.Request.Context(), uiFeedFetchTimeout)
	defer cancel()

	mutedWords := h.mutedWordsFor(ctx, did, sessionID)
	feedPage := &feed.FeedPage{Posts: []feed.PostView{}}
	feedErr, sessionExpired := false, false
	if fetchedPage, err := h.feedClient.GetCustomFeed(
		ctx, did, sessionID, feedURI, cursor, uiFeedLimit, mutedWords,
	); err != nil {
		if h.sessionDead(did, sessionID, err) {
			sessionExpired = true
		} else {
			slog.Error("GetCustomFeed (partial)", "did", did, "uri", feedURI, "err", err)
			feedErr = true
		}
	} else {
		feedPage = fetchedPage
	}

	data := LayoutData{
		FeedPage:      feedPage,
		FeedType:      "custom",
		FeedCustomURI: feedURI,
		SentinelURL:   customFeedSentinelURL(feedURI, feedPage.NextCursor),
	}
	if cursor == "" {
		if sessionExpired {
			data.SessionExpired = true
		} else if feedErr {
			data.FeedError = true
			data.RetryURL = c.Request.URL.RequestURI()
		}
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

// HandleFeedTabsPartial serves the saved-feeds tab bar as a lazy HTMX region for the
// home shell (hx-get on load). It performs the one getPreferences call the shell
// deferred. On failure (PDS degradation) it degrades to a Following-only tab bar with
// a small note rather than an error — the same server-side fallback the synchronous
// path had, now with a face. Rendered as "feed-tabs-inner" (the tab buttons), swapped
// into the <nav class="feed-tabs"> the layout already emitted.
func (h *UIHandler) HandleFeedTabsPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), uiFeedFetchTimeout)
	defer cancel()

	data := LayoutData{
		// The shell always lands on Following, so mark that tab active by default.
		FeedType: "following",
	}
	if prefs, err := h.feedClient.GetPreferences(ctx, did, sessionID); err != nil {
		slog.Warn("GetPreferences (tabs partial) failed; degrading to Following-only", "did", did, "err", err)
		data.SavedFeeds = []feed.SavedFeed{{DisplayName: "Following", IsTimeline: true}}
		data.TabsDegraded = true
	} else {
		data.SavedFeeds = prefs.SavedFeeds
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, "feed-tabs-inner", data); err != nil {
		slog.Error("render feed tabs partial", "err", err)
	}
}

// HandleRecentTagsPartial serves the right-rail recent-tags list as a lazy HTMX region
// for the home shell. The underlying query is Postgres-local and fast; it is deferred
// only for pattern consistency with the other shell regions (one lazy-load story for
// the whole page). Rendered as "recent-tags-inner", swapped into the rail container.
func (h *UIHandler) HandleRecentTagsPartial(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID (recent-tags partial)", "did", did, "err", err)
		c.Status(http.StatusInternalServerError)
		return
	}

	data := LayoutData{RecentTags: h.recentTagsFor(c.Request.Context(), user.ID)}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, "recent-tags-inner", data); err != nil {
		slog.Error("render recent tags partial", "err", err)
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
	recentTags := h.recentTagsFor(c.Request.Context(), user.ID)

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

	// Fetch the two upstream inputs for the chrome — the saved-feeds tab bar and the
	// unread-notification badge — concurrently, so a page render pays one round-trip
	// rather than two. Each degrades independently: saved feeds fall back to
	// Following-only, the badge falls back to 0 (hidden). Neither breaks the page.
	var (
		wg          sync.WaitGroup
		savedFeeds  []feed.SavedFeed
		mutedWords  []feed.MutedWord
		unreadCount int64
	)
	ctx := c.Request.Context()

	wg.Add(2)
	go func() {
		defer wg.Done()
		// One getPreferences call serves both the saved-feeds tab bar and muted-word
		// filtering for any feed this render also fetches (Following on home, thread
		// posts). On failure, degrade to a Following-only tab bar and no muted-word
		// filtering — neither breaks the page.
		prefs, err := h.feedClient.GetPreferences(ctx, did, sessionID)
		if err != nil {
			slog.Warn("GetPreferences failed, using following-only tab bar and no muted-word filter", "did", did, "err", err)
			savedFeeds = []feed.SavedFeed{{DisplayName: "Following", IsTimeline: true}}
			return
		}
		savedFeeds = prefs.SavedFeeds
		mutedWords = prefs.MutedWords
	}()
	go func() {
		defer wg.Done()
		count, err := h.feedClient.GetUnreadCount(ctx, did, sessionID)
		if err != nil {
			slog.Warn("GetUnreadCount failed, hiding notification badge", "did", did, "err", err)
			count = 0
		}
		unreadCount = count
	}()
	wg.Wait()

	return LayoutData{
		User: PageUser{
			DID:    did,
			Handle: handle,
			Plan:   user.Plan,
			Avatar: avatar,
		},
		Theme:       theme,
		CSRFToken:   auth.CSRFToken(sessionID, h.secret),
		RecentTags:  recentTags,
		SavedFeeds:  savedFeeds,
		MutedWords:  mutedWords,
		UnreadCount: unreadCount,
	}
}

// recentTagsFor returns the user's last unique hashtags (right-rail rota) from the
// local post_history table, degrading to an empty slice on error. Shared by
// buildLayoutBase (synchronous pages) and HandleRecentTagsPartial (home shell).
func (h *UIHandler) recentTagsFor(ctx context.Context, userID int32) []string {
	tagRows, err := h.queries.GetRecentTagsByUser(ctx, pgtype.Int4{Int32: userID, Valid: true})
	if err != nil {
		slog.Error("GetRecentTagsByUser", "user_id", userID, "err", err)
	}
	recentTags := make([]string, 0, len(tagRows))
	for _, row := range tagRows {
		recentTags = append(recentTags, row.Tag)
	}
	return recentTags
}

// fetchMutedWords fetches the user's muted words for feed content filtering, degrading to
// nil (no filtering) on any error — muting must never break a feed fetch. Shared by the
// UI-partial, profile, and JSON API handlers, which don't build the full layout (and so
// have no MutedWords cached from buildLayoutBase's single getPreferences call).
func fetchMutedWords(ctx context.Context, client *feed.Client, did, sessionID string) []feed.MutedWord {
	words, err := client.GetMutedWords(ctx, did, sessionID)
	if err != nil {
		slog.Warn("GetMutedWords failed; feed rendered unfiltered", "did", did, "err", err)
		return nil
	}
	return words
}

// mutedWordsFor is the UIHandler-scoped convenience wrapper around fetchMutedWords.
func (h *UIHandler) mutedWordsFor(ctx context.Context, did, sessionID string) []feed.MutedWord {
	return fetchMutedWords(ctx, h.feedClient, did, sessionID)
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

	// Mirror the server-side cap in the UI: a free user at the limit gets a disabled
	// Add button with an inline message. Paid users are never capped. This is cosmetic
	// — the create handlers re-check authoritatively regardless of what the UI shows.
	canAdd := user.Plan == "paid" || len(templates) < freeTemplateLimit

	data := TemplatesPageData{
		LayoutData:     h.buildLayoutBase(c, did, sessionID, user),
		Templates:      templates,
		CanAddTemplate: canAdd,
		LimitMessage:   freeTemplateLimitMessage,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplTemplates.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render templates page", "err", err)
	}
}

// HandleSettingsPage renders the settings page (Account + Theme selector).
// Theme and plan are carried in the LayoutData envelope, so the template drives
// the selected/locked card states directly from .Theme and .User.Plan.
func (h *UIHandler) HandleSettingsPage(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, ok := h.resolveUserForTemplates(c, did)
	if !ok {
		return
	}

	data := h.buildLayoutBase(c, did, sessionID, user)

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplSettings.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render settings page", "err", err)
	}
}

// HandleAdminStats renders the owner-only stats dashboard. RequireAdmin has already
// verified the caller is the ADMIN_DID owner and seeded the DID/session ID, so this
// handler does no further gating. Both stats queries are single-pass aggregates.
func (h *UIHandler) HandleAdminStats(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in admin stats", "did", did, "err", err)
		h.Handle500(c)
		return
	}

	stats, err := h.queries.GetAdminStats(c.Request.Context())
	if err != nil {
		slog.Error("GetAdminStats", "err", err)
		h.Handle500(c)
		return
	}
	content, err := h.queries.GetContentStats(c.Request.Context())
	if err != nil {
		slog.Error("GetContentStats", "err", err)
		h.Handle500(c)
		return
	}

	data := AdminStatsData{
		LayoutData:     h.buildLayoutBase(c, did, sessionID, user),
		TotalUsers:     stats.TotalUsers,
		NewToday:       stats.NewToday,
		NewThisWeek:    stats.NewThisWeek,
		DAU:            stats.Dau,
		WAU:            stats.Wau,
		MAU:            stats.Mau,
		TotalTemplates: content.TotalTemplates,
		TotalPosts:     content.TotalPosts,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplAdmin.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render admin stats page", "err", err)
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

	// Free-tier cap. Same authoritative check as the JSON API: a free user who forces
	// this POST past the disabled Add button is blocked with 403. The JSON error body
	// renders through the inline #add-template-error path (onAddTemplateResponse).
	atCap, err := freeUserAtTemplateCap(c.Request.Context(), h.queries, user)
	if err != nil {
		slog.Error("CountTemplatesByUser (web) failed", "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if atCap {
		c.JSON(http.StatusForbidden, gin.H{"error": freeTemplateLimitMessage})
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

	threadView, err := h.feedClient.GetThread(c.Request.Context(), did, sessionID, uri, data.MutedWords)
	if err != nil {
		// A dead session can't be recovered by re-rendering — send the user to log in
		// rather than showing a full page whose only content is an error (Part 2c).
		if h.sessionDead(did, sessionID, err) {
			c.Redirect(http.StatusFound, "/login")
			return
		}
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

// HandleNotificationsPage renders the notifications view.
//
// Without a cursor it renders the full three-column layout and — after fetching, so
// the page still shows which items were unread — calls UpdateSeen to clear the badge.
// With a cursor it serves just the "notifications-more" fragment for infinite scroll
// (and does NOT re-run UpdateSeen).
//
// Badge freshness: the server renders the count at page-render time; a client-side
// poll (static/app.js, every 60s, paused on hidden tabs) then keeps the left-rail
// badge current while the app stays open. There is still no server push — a brand-new
// notification surfaces within one poll interval, not instantly.
func (h *UIHandler) HandleNotificationsPage(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")

	// Pagination request: return only the row fragment. No layout, no UpdateSeen.
	if cursor != "" {
		page := &feed.NotificationPage{Notifications: []feed.Notification{}}
		if fetched, err := h.feedClient.GetNotifications(
			c.Request.Context(), did, sessionID, cursor, uiFeedLimit,
		); err != nil {
			slog.Error("GetNotifications (partial)", "did", did, "err", err)
		} else {
			page = fetched
		}

		data := NotificationsPageData{
			LayoutData:    LayoutData{SentinelURL: notificationsSentinelURL(page.NextCursor)},
			Notifications: page.Notifications,
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := h.tmplNotifications.ExecuteTemplate(c.Writer, "notifications-more", data); err != nil {
			slog.Error("render notifications partial", "err", err)
		}
		return
	}

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		slog.Error("GetUserByDID in notifications handler", "did", did, "err", err)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	data := NotificationsPageData{
		LayoutData: h.buildLayoutBase(c, did, sessionID, user),
	}

	page, err := h.feedClient.GetNotifications(c.Request.Context(), did, sessionID, "", uiFeedLimit)
	if err != nil {
		if h.sessionDead(did, sessionID, err) {
			c.Redirect(http.StatusFound, "/login")
			return
		}
		slog.Error("GetNotifications in notifications handler", "did", did, "err", err)
		data.Error = "Unable to load notifications. Please try again."
	} else {
		data.Notifications = page.Notifications
		data.SentinelURL = notificationsSentinelURL(page.NextCursor)
	}

	// Mark seen only after fetching, so the rows above still reflect which items were
	// unread. Best-effort: a failure to clear the badge must not break the page.
	if err := h.feedClient.UpdateSeen(c.Request.Context(), did, sessionID); err != nil {
		slog.Warn("UpdateSeen failed", "did", did, "err", err)
	} else {
		// The badge on the page we're rendering is now stale — we just cleared it.
		data.UnreadCount = 0
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplNotifications.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render notifications page", "err", err)
	}
}

func notificationsSentinelURL(nextCursor string) string {
	if nextCursor == "" {
		return ""
	}
	return "/notifications?cursor=" + url.QueryEscape(nextCursor)
}

func followingSentinelURL(nextCursor string) string {
	if nextCursor == "" {
		return ""
	}
	return "/feed/following?cursor=" + url.QueryEscape(nextCursor)
}

func hashtagSentinelURL(tags []string, author, nextCursor string) string {
	if nextCursor == "" || len(tags) == 0 {
		return ""
	}
	u := "/feed/hashtags?tags=" + strings.Join(tags, ",")
	if author != "" {
		u += "&author=" + url.QueryEscape(author)
	}
	return u + "&cursor=" + url.QueryEscape(nextCursor)
}

func customFeedSentinelURL(feedURI, nextCursor string) string {
	if nextCursor == "" || feedURI == "" {
		return ""
	}
	return "/feed/custom?uri=" + url.QueryEscape(feedURI) + "&cursor=" + url.QueryEscape(nextCursor)
}
