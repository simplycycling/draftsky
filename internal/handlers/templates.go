package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// templateResponse is the clean JSON representation returned to clients.
// It avoids exposing pgtype wrappers directly.
type templateResponse struct {
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	Suffix    string `json:"suffix"`
	Position  int32  `json:"position"`
	CreatedAt string `json:"created_at,omitempty"`
}

// TemplateHandler holds dependencies for the template API endpoints.
type TemplateHandler struct {
	queries *db.Queries
	pool    *pgxpool.Pool
}

// NewTemplateHandler constructs a TemplateHandler.
func NewTemplateHandler(queries *db.Queries, pool *pgxpool.Pool) *TemplateHandler {
	return &TemplateHandler{queries: queries, pool: pool}
}

// toResponse converts a sqlc Template into its JSON-safe representation.
func toResponse(t db.Template) templateResponse {
	r := templateResponse{
		ID:     t.ID,
		Name:   t.Name,
		Suffix: t.Suffix,
	}
	if t.Position.Valid {
		r.Position = t.Position.Int32
	}
	if t.CreatedAt.Valid {
		r.CreatedAt = t.CreatedAt.Time.UTC().Format(time.RFC3339)
	}
	return r
}

// resolveUser looks up the user row by DID and writes an error response if it
// fails. The second return is false when the caller should abort the handler.
func (h *TemplateHandler) resolveUser(c *gin.Context, did string) (db.User, bool) {
	user, err := h.queries.GetUserByDID(c.Request.Context(), did)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		} else {
			slog.Error("GetUserByDID failed", "did", did, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return db.User{}, false
	}
	return user, true
}

// parseTemplateID parses the :id route parameter as an int32.
func parseTemplateID(c *gin.Context) (int32, bool) {
	v, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid template id"})
		return 0, false
	}
	return int32(v), true
}

// isUniqueViolation returns true when err is a PostgreSQL unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ---------------------------------------------------------------------------
// GET /api/templates
// ---------------------------------------------------------------------------

// HandleGetComposerTemplates is an alias for HandleGetTemplates used by the
// composer modal. Keeping it as a separate route makes the endpoint discoverable
// and allows independent rate-limiting in a future phase.
func (h *TemplateHandler) HandleGetComposerTemplates(c *gin.Context) {
	h.HandleGetTemplates(c)
}

// HandleGetTemplates returns all templates owned by the authenticated user,
// ordered by position ascending.
func (h *TemplateHandler) HandleGetTemplates(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	user, ok := h.resolveUser(c, did)
	if !ok {
		return
	}

	rows, err := h.queries.ListTemplatesByUser(c.Request.Context(),
		pgtype.Int4{Int32: user.ID, Valid: true})
	if err != nil {
		slog.Error("ListTemplatesByUser failed", "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch templates"})
		return
	}

	resp := make([]templateResponse, len(rows))
	for i, t := range rows {
		resp[i] = toResponse(t)
	}
	c.JSON(http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// POST /api/templates
// ---------------------------------------------------------------------------

type createTemplateRequest struct {
	Name     string `json:"name"   binding:"required"`
	Suffix   string `json:"suffix" binding:"required"`
	Position *int32 `json:"position"`
}

// HandleCreateTemplate creates a new template for the authenticated user.
// Returns 409 if a template with the same name already exists.
func (h *TemplateHandler) HandleCreateTemplate(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	var req createTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len([]rune(req.Name)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template name must be 100 characters or fewer"})
		return
	}
	if len([]rune(req.Suffix)) > 250 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template suffix must be 250 characters or fewer"})
		return
	}

	user, ok := h.resolveUser(c, did)
	if !ok {
		return
	}

	position := pgtype.Int4{Int32: 0, Valid: true}
	if req.Position != nil {
		position = pgtype.Int4{Int32: *req.Position, Valid: true}
	}

	t, err := h.queries.CreateTemplate(c.Request.Context(), db.CreateTemplateParams{
		UserID:   pgtype.Int4{Int32: user.ID, Valid: true},
		Name:     req.Name,
		Suffix:   req.Suffix,
		Position: position,
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a template with that name already exists"})
			return
		}
		slog.Error("CreateTemplate failed", "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create template"})
		return
	}

	c.JSON(http.StatusCreated, toResponse(t))
}

// ---------------------------------------------------------------------------
// PUT /api/templates/:id
// ---------------------------------------------------------------------------

type updateTemplateRequest struct {
	Name     string `json:"name"   binding:"required"`
	Suffix   string `json:"suffix" binding:"required"`
	Position *int32 `json:"position"`
}

// HandleUpdateTemplate updates the name and suffix of a template and,
// if position is supplied, its display order as well.
// Returns 404 when the template does not exist or belongs to another user.
func (h *TemplateHandler) HandleUpdateTemplate(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	id, ok := parseTemplateID(c)
	if !ok {
		return
	}

	var req updateTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len([]rune(req.Name)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template name must be 100 characters or fewer"})
		return
	}
	if len([]rune(req.Suffix)) > 250 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template suffix must be 250 characters or fewer"})
		return
	}

	user, ok := h.resolveUser(c, did)
	if !ok {
		return
	}

	userID := pgtype.Int4{Int32: user.ID, Valid: true}

	t, err := h.queries.UpdateTemplate(c.Request.Context(), db.UpdateTemplateParams{
		ID:     id,
		UserID: userID,
		Name:   req.Name,
		Suffix: req.Suffix,
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
		slog.Error("UpdateTemplate failed", "template_id", id, "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update template"})
		return
	}

	if req.Position != nil {
		t, err = h.queries.UpdateTemplatePosition(c.Request.Context(), db.UpdateTemplatePositionParams{
			ID:       id,
			UserID:   userID,
			Position: pgtype.Int4{Int32: *req.Position, Valid: true},
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
				return
			}
			slog.Error("UpdateTemplatePosition failed", "template_id", id, "user_id", user.ID, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update template position"})
			return
		}
	}

	c.JSON(http.StatusOK, toResponse(t))
}

// ---------------------------------------------------------------------------
// DELETE /api/templates/:id
// ---------------------------------------------------------------------------

// HandleDeleteTemplate deletes a template after verifying it belongs to the
// authenticated user. Returns 404 if the template does not exist or is owned
// by someone else (no information leakage either way).
func (h *TemplateHandler) HandleDeleteTemplate(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	id, ok := parseTemplateID(c)
	if !ok {
		return
	}

	user, ok := h.resolveUser(c, did)
	if !ok {
		return
	}

	userID := pgtype.Int4{Int32: user.ID, Valid: true}

	// GetTemplate doubles as the ownership check. DeleteTemplate is :exec and
	// returns no rows, so we cannot distinguish "not found" from "deleted" there.
	if _, err := h.queries.GetTemplate(c.Request.Context(), db.GetTemplateParams{
		ID:     id,
		UserID: userID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
			return
		}
		slog.Error("GetTemplate (pre-delete) failed", "template_id", id, "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if err := h.queries.DeleteTemplate(c.Request.Context(), db.DeleteTemplateParams{
		ID:     id,
		UserID: userID,
	}); err != nil {
		slog.Error("DeleteTemplate failed", "template_id", id, "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete template"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// PUT /api/templates/reorder
// ---------------------------------------------------------------------------

type reorderRequest struct {
	IDs []int32 `json:"ids" binding:"required"`
}

// HandleReorderTemplates accepts an ordered slice of template IDs. Each ID is
// assigned a position equal to its index in the slice. All updates run inside
// a single transaction so a partial failure rolls back the whole reorder.
func (h *TemplateHandler) HandleReorderTemplates(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)

	var req reorderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids must not be empty"})
		return
	}

	user, ok := h.resolveUser(c, did)
	if !ok {
		return
	}

	userID := pgtype.Int4{Int32: user.ID, Valid: true}

	tx, err := h.pool.Begin(c.Request.Context())
	if err != nil {
		slog.Error("failed to begin reorder transaction", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer func() {
		if rbErr := tx.Rollback(context.Background()); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Warn("reorder rollback error", "err", rbErr)
		}
	}()

	txq := h.queries.WithTx(tx)

	for i, id := range req.IDs {
		_, err := txq.UpdateTemplatePosition(c.Request.Context(), db.UpdateTemplatePositionParams{
			ID:       id,
			UserID:   userID,
			Position: pgtype.Int4{Int32: int32(i), Valid: true},
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "template not found", "id": id})
				return
			}
			slog.Error("UpdateTemplatePosition failed during reorder",
				"template_id", id, "user_id", user.ID, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder templates"})
			return
		}
	}

	if err := tx.Commit(c.Request.Context()); err != nil {
		slog.Error("failed to commit reorder transaction", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder templates"})
		return
	}

	// Return the updated list so clients don't need a separate GET.
	rows, err := h.queries.ListTemplatesByUser(c.Request.Context(), userID)
	if err != nil {
		slog.Error("ListTemplatesByUser failed after reorder", "user_id", user.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch updated templates"})
		return
	}

	resp := make([]templateResponse, len(rows))
	for i, t := range rows {
		resp[i] = toResponse(t)
	}
	c.JSON(http.StatusOK, resp)
}
