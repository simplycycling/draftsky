package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rsherman/draftsky/internal/bluesky"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// PostHandler holds dependencies for the post API endpoint.
type PostHandler struct {
	queries *db.Queries
	poster  *bluesky.Poster
}

// NewPostHandler constructs a PostHandler.
func NewPostHandler(queries *db.Queries, poster *bluesky.Poster) *PostHandler {
	return &PostHandler{queries: queries, poster: poster}
}

type createPostRequest struct {
	Text       string `json:"text"        binding:"required"`
	TemplateID *int32 `json:"template_id"`
}

type createPostResponse struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// HandleCreatePost composes and submits a post to Bluesky.
// If template_id is provided, the template's suffix is appended to the post text.
func (h *PostHandler) HandleCreatePost(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)

	var req createPostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var suffix string
	if req.TemplateID != nil {
		user, err := h.queries.GetUserByDID(c.Request.Context(), did)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			} else {
				slog.Error("GetUserByDID failed", "did", did, "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			}
			return
		}

		tmpl, err := h.queries.GetTemplate(c.Request.Context(), db.GetTemplateParams{
			ID:     *req.TemplateID,
			UserID: pgtype.Int4{Int32: user.ID, Valid: true},
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
			} else {
				slog.Error("GetTemplate failed", "template_id", req.TemplateID, "did", did, "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			}
			return
		}
		suffix = tmpl.Suffix
	}

	result, err := h.poster.Post(c.Request.Context(), did, sessionID, req.Text, suffix)
	if err != nil {
		slog.Error("bluesky post failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to submit post"})
		return
	}

	c.JSON(http.StatusCreated, createPostResponse{URI: result.URI, CID: result.CID})
}
