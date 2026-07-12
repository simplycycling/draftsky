package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

// hashtagFeedRouter mounts HandleGetHashtagFeed behind a seed middleware that stands in
// for RequireAuth by populating the DID/session. The FeedHandler is backed by a
// feed.Client with a nil app: every test here exercises request validation, which
// short-circuits before any session resume, so no OAuth plumbing is needed.
func hashtagFeedRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewFeedHandler(feed.New(nil))
	seed := func(c *gin.Context) {
		c.Set(middleware.ContextKeyDID, "did:plc:me")
		c.Set(middleware.ContextKeySessionID, "sess")
	}
	r.GET("/api/feed/hashtags", seed, h.HandleGetHashtagFeed)
	return r
}

// TestHashtagFeed_AuthorValidation verifies the optional author param is rejected with a
// 400 when it is not a syntactically valid handle or DID — before any upstream call. A
// bad author fails validation even without tags, since author is checked first.
func TestHashtagFeed_AuthorValidation(t *testing.T) {
	r := hashtagFeedRouter()

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"bad author with tags", "/api/feed/hashtags?tags=njdevils&author=not%20a%20handle", http.StatusBadRequest},
		{"bad author no tags", "/api/feed/hashtags?author=foo%2Fbar", http.StatusBadRequest},
		{"missing tags valid author", "/api/feed/hashtags?author=roger.bsky.social", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != tc.want {
				t.Errorf("%s: status = %d, want %d (body: %s)", tc.url, w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestHashtagSentinelURL covers the infinite-scroll sentinel, including the optional
// author param (present only on the "by @author" hashtag feed).
func TestHashtagSentinelURL(t *testing.T) {
	if got := hashtagSentinelURL([]string{"njdevils"}, "", ""); got != "" {
		t.Errorf("empty cursor should yield empty sentinel, got %q", got)
	}
	if got := hashtagSentinelURL(nil, "", "cur"); got != "" {
		t.Errorf("no tags should yield empty sentinel, got %q", got)
	}
	if got := hashtagSentinelURL([]string{"njdevils", "nhl"}, "", "cur:1"); got != "/feed/hashtags?tags=njdevils,nhl&cursor=cur%3A1" {
		t.Errorf("no-author sentinel = %q", got)
	}
	got := hashtagSentinelURL([]string{"njdevils"}, "roger.bsky.social", "cur:1")
	want := "/feed/hashtags?tags=njdevils&author=roger.bsky.social&cursor=cur%3A1"
	if got != want {
		t.Errorf("author sentinel = %q, want %q", got, want)
	}
}
