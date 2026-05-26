package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/time/rate"

	"github.com/rsherman/draftsky/internal/bluesky"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// postRateLimit is 10 posts per minute per user.
const postRateLimit = 10
const postRateBurst = 10

// PostHandler holds dependencies for the post API endpoint.
type PostHandler struct {
	queries  *db.Queries
	poster   *bluesky.Poster
	limiters sync.Map // DID → *rate.Limiter
}

// NewPostHandler constructs a PostHandler.
func NewPostHandler(queries *db.Queries, poster *bluesky.Poster) *PostHandler {
	return &PostHandler{queries: queries, poster: poster}
}

func (h *PostHandler) limiterFor(did string) *rate.Limiter {
	v, _ := h.limiters.LoadOrStore(did, rate.NewLimiter(rate.Every(time.Minute/postRateLimit), postRateBurst))
	return v.(*rate.Limiter)
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

	if !h.limiterFor(did).Allow() {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "You are posting too quickly — please wait a moment and try again."})
		return
	}

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
		if bluesky.IsRateLimitError(err) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Bluesky is rate limiting your account — try again shortly."})
			return
		}
		slog.Error("bluesky post failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to submit post"})
		return
	}

	// Write post_history — non-fatal; best-effort for recent-tags feature.
	// Use a detached context so the goroutine outlives the HTTP request.
	go func() {
		ctx := context.Background()
		user, dbErr := h.queries.GetUserByDID(ctx, did)
		if dbErr != nil {
			slog.Warn("post_history: GetUserByDID failed", "did", did, "err", dbErr)
			return
		}
		fullText := req.Text
		if suffix != "" {
			fullText = req.Text + " " + suffix
		}
		hashtags := bluesky.ExtractHashtags(fullText)
		if len(hashtags) == 0 {
			return
		}
		if _, insErr := h.queries.CreatePostHistory(ctx, db.CreatePostHistoryParams{
			UserID:   pgtype.Int4{Int32: user.ID, Valid: true},
			Uri:      result.URI,
			Hashtags: hashtags,
		}); insErr != nil {
			slog.Warn("post_history: insert failed", "did", did, "err", insErr)
		}
	}()

	c.JSON(http.StatusCreated, createPostResponse{URI: result.URI, CID: result.CID})
}
