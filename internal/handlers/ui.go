package handlers

import (
	"html/template"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/auth"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// PageUser is the user representation passed to all page templates.
type PageUser struct {
	DID    string
	Handle string
	Plan   string
	Avatar string // empty when not yet fetched from Bluesky
}

// LayoutData is the data envelope passed to every page rendered with layout.html.
type LayoutData struct {
	User       PageUser
	Theme      string
	RecentTags []string
}

// UIHandler renders HTML pages.
type UIHandler struct {
	queries   *db.Queries
	secret    []byte
	tmplHome  *template.Template
	tmplLogin *template.Template
}

// NewUIHandler parses all page templates and returns a ready UIHandler.
// Returns an error if any template file cannot be parsed — callers should
// treat this as fatal at startup.
func NewUIHandler(queries *db.Queries, secret []byte) (*UIHandler, error) {
	tmplHome, err := template.ParseFiles(
		"templates/layout.html",
		"templates/partials/composer.html",
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
		queries:   queries,
		secret:    secret,
		tmplHome:  tmplHome,
		tmplLogin: tmplLogin,
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

// HandleHome renders the three-column layout with an empty centre placeholder.
// RequireSession middleware ensures only authenticated users reach this handler.
func (h *UIHandler) HandleHome(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

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
		// Non-fatal: log and show empty right rail.
		slog.Error("GetRecentTagsByUser", "user_id", user.ID, "err", err)
	}
	tags := make([]string, 0, len(tagRows))
	for _, row := range tagRows {
		tags = append(tags, row.Tag)
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

	data := LayoutData{
		User: PageUser{
			DID:    did,
			Handle: handle,
			Plan:   user.Plan,
		},
		Theme:      theme,
		RecentTags: tags,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmplHome.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		slog.Error("render home template", "err", err)
	}
}
