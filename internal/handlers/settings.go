package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// allowedThemes is the server-side allowlist of theme keys. It mirrors the
// Themes table in CLAUDE.md. A theme key that is not present here is rejected
// with 400 before any plan check or database write.
var allowedThemes = map[string]bool{
	"ocean":    true,
	"slate":    true,
	"amber":    true,
	"graphite": true,
}

// settingsStore is the narrow slice of the sqlc query surface the settings
// endpoint needs. Declaring it as an interface lets the endpoint be unit-tested
// with a fake, without a live PostgreSQL. *db.Queries satisfies it.
type settingsStore interface {
	GetUserByDID(ctx context.Context, did string) (db.User, error)
	UpdateUserTheme(ctx context.Context, arg db.UpdateUserThemeParams) (db.User, error)
}

// SettingsHandler holds dependencies for the settings JSON API endpoints.
type SettingsHandler struct {
	queries settingsStore
}

// NewSettingsHandler constructs a SettingsHandler.
func NewSettingsHandler(queries settingsStore) *SettingsHandler {
	return &SettingsHandler{queries: queries}
}

// HandleUpdateTheme updates the authenticated user's theme.
//
// This is an HTMX-triggered mutation, so the body is form-encoded (Gotcha 2):
// the theme key is read with c.PostForm, never ShouldBindJSON.
//
// Enforcement lives here, not in the UI. The UI locks paid theme cards for free
// users, but that lock is cosmetic — the server re-checks the plan and returns
// 403 for any free user regardless of what the client submitted, on the same
// principle as server-side template length validation.
func (h *SettingsHandler) HandleUpdateTheme(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	// Validate the theme key against the allowlist first, so a malformed key is
	// a 400 before we touch the database or consider the plan.
	theme := c.PostForm("theme")
	if !allowedThemes[theme] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid theme"})
		return
	}

	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}
		slog.Error("GetUserByDID in theme update", "did", did, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Server-side plan enforcement — the authoritative gate. Ocean is the only
	// free theme; everything else requires a paid plan.
	if user.Plan != "paid" {
		c.JSON(http.StatusForbidden, gin.H{"error": "themes are a paid feature"})
		return
	}

	if _, err := h.queries.UpdateUserTheme(c.Request.Context(), db.UpdateUserThemeParams{
		Did:   did,
		Theme: theme,
	}); err != nil {
		slog.Error("UpdateUserTheme", "did", did, "theme", theme, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update theme"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"theme": theme})
}
