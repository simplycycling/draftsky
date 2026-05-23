package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

const (
	defaultFeedLimit = 50
	maxFeedLimit     = 100
)

// FeedHandler holds dependencies for the feed API endpoints.
type FeedHandler struct {
	client *feed.Client
}

// NewFeedHandler constructs a FeedHandler.
func NewFeedHandler(client *feed.Client) *FeedHandler {
	return &FeedHandler{client: client}
}

// parseLimit reads the "limit" query parameter, applying the default and
// capping at the maximum. Negative or missing values fall back to the default.
func parseLimit(c *gin.Context) int {
	raw := c.Query("limit")
	if raw == "" {
		return defaultFeedLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultFeedLimit
	}
	if n > maxFeedLimit {
		return maxFeedLimit
	}
	return n
}

// HandleGetFollowingFeed returns a page of the authenticated user's Following feed.
//
// Query params:
//   - cursor  (string, optional) — opaque pagination cursor from a previous response
//   - limit   (int, optional)    — page size, default 50, max 100
func (h *FeedHandler) HandleGetFollowingFeed(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")
	limit := parseLimit(c)

	page, err := h.client.GetFollowingFeed(c.Request.Context(), did, sessionID, cursor, limit)
	if err != nil {
		slog.Error("GetFollowingFeed failed", "did", did, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch following feed"})
		return
	}

	c.JSON(http.StatusOK, page)
}

// HandleGetHashtagFeed returns a merged, deduped, recency-sorted page of posts
// matching one or more hashtags.
//
// Query params:
//   - tags    (string, required)  — comma-separated list of hashtags (with or without leading #)
//   - cursor  (string, optional)  — indexedAt timestamp; only posts strictly before this are returned
//   - limit   (int, optional)     — page size, default 50, max 100
func (h *FeedHandler) HandleGetHashtagFeed(c *gin.Context) {
	did := c.GetString(middleware.ContextKeyDID)
	sessionID := c.GetString(middleware.ContextKeySessionID)
	cursor := c.Query("cursor")
	limit := parseLimit(c)

	rawTags := c.Query("tags")
	if rawTags == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tags query parameter is required"})
		return
	}

	// Split on commas, strip whitespace, drop empties.
	parts := strings.Split(rawTags, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tags query parameter is required"})
		return
	}

	page, err := h.client.GetHashtagFeed(c.Request.Context(), did, sessionID, tags, cursor, limit)
	if err != nil {
		slog.Error("GetHashtagFeed failed", "did", did, "tags", tags, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch hashtag feed"})
		return
	}

	c.JSON(http.StatusOK, page)
}
