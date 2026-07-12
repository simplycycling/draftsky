package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/rsherman/draftsky/internal/feed"
	"github.com/rsherman/draftsky/internal/middleware"
)

// typeaheadRouter mounts HandleActorTypeahead behind the real operations rate limiter,
// after a seed middleware that stands in for RequireAuth by populating the DID. The
// ProfileHandler is backed by a feed.Client with a nil app: a blank q short-circuits
// before any session resume, so no OAuth plumbing is needed for these tests.
func typeaheadRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewProfileHandler(nil, feed.New(nil))
	seed := func(c *gin.Context) {
		c.Set(middleware.ContextKeyDID, "did:plc:me")
		c.Set(middleware.ContextKeySessionID, "sess")
	}
	limiter := middleware.NewOperationsRateLimiter()
	r.GET("/api/actors/typeahead", seed, limiter.Middleware(), h.HandleActorTypeahead)
	return r
}

// TestActorTypeahead_EmptyQueryShape verifies the endpoint returns an empty JSON array
// (200) for a blank query — never null, never an error — so the client can call it
// without special-casing the no-query state.
func TestActorTypeahead_EmptyQueryShape(t *testing.T) {
	r := typeaheadRouter()

	for _, url := range []string{"/api/actors/typeahead", "/api/actors/typeahead?q=", "/api/actors/typeahead?q=%20%20"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))

		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", url, w.Code)
		}
		var got []feed.ActorSuggestion
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: response not a JSON array: %v (body %q)", url, err, w.Body.String())
		}
		if len(got) != 0 {
			t.Errorf("%s: expected empty array, got %d rows", url, len(got))
		}
		// Must serialise as [] and not null (Gotcha 9).
		if body := w.Body.String(); body != "[]" && body != "[]\n" {
			t.Errorf("%s: expected [] body, got %q", url, body)
		}
	}
}

// TestActorTypeahead_LimiterApplied confirms the route rides the operations rate
// limiter: after the 60-request burst is exhausted, the 61st request is rejected with
// 429. Empty-q requests are used so the limiter is exercised without touching the PDS.
func TestActorTypeahead_LimiterApplied(t *testing.T) {
	r := typeaheadRouter()

	for i := 0; i < 60; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/actors/typeahead?q=", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (within burst)", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/actors/typeahead?q=", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("61st request: status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("429 response missing Retry-After header")
	}
}
