package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
)

// TestBuildShellLayout_NoUpstreamInputs verifies the home shell's LayoutData is built
// purely from local user fields — no saved feeds, recent tags, muted words, unread
// count, or feed page. buildShellLayout takes NO feed client, so it is structurally
// impossible for it (and therefore the shell home render) to make an upstream/PDS call;
// this test pins the shape that guarantee produces. It is the unit-level half of "GET /
// makes zero upstream calls" (the template half is TestHomeShellRenders below).
func TestBuildShellLayout_NoUpstreamInputs(t *testing.T) {
	user := db.User{
		ID:     7,
		Did:    "did:plc:me",
		Handle: pgtype.Text{String: "me.bsky.social", Valid: true},
		Plan:   "free",
		Theme:  "ocean",
		Avatar: pgtype.Text{String: "https://example.com/a.jpg", Valid: true},
	}

	data := buildShellLayout("did:plc:me", "sess-1", user, []byte("secret"))

	if !data.LazyChrome {
		t.Error("LazyChrome = false, want true (the shell defers all upstream regions)")
	}
	if data.SavedFeeds != nil {
		t.Errorf("SavedFeeds = %v, want nil (deferred to /feed/tabs)", data.SavedFeeds)
	}
	if data.RecentTags != nil {
		t.Errorf("RecentTags = %v, want nil (deferred to /feed/recent-tags)", data.RecentTags)
	}
	if data.MutedWords != nil {
		t.Errorf("MutedWords = %v, want nil (the feed partial fetches its own)", data.MutedWords)
	}
	if data.FeedPage != nil {
		t.Errorf("FeedPage = %v, want nil (deferred to /feed/following)", data.FeedPage)
	}
	if data.UnreadCount != 0 {
		t.Errorf("UnreadCount = %d, want 0 (filled by the client poll)", data.UnreadCount)
	}
	if data.User.Handle != "me.bsky.social" {
		t.Errorf("User.Handle = %q, want me.bsky.social", data.User.Handle)
	}
	if data.CSRFToken == "" {
		t.Error("CSRFToken is empty; the shell must still carry a CSRF token")
	}
}

// TestBuildShellLayout_FreeUserPaidThemeFallsBack pins the free-user theme guard, which
// the shell path must preserve now that it no longer routes through buildLayoutBase.
func TestBuildShellLayout_FreeUserPaidThemeFallsBack(t *testing.T) {
	user := db.User{Did: "did:plc:me", Plan: "free", Theme: "amber"}
	if got := buildShellLayout("did:plc:me", "s", user, []byte("k")).Theme; got != "ocean" {
		t.Errorf("Theme = %q, want ocean (free users are locked to ocean)", got)
	}
}

// TestHomeShellRenders exercises the home "layout" end-to-end with shell data and
// asserts the three lazy regions (tab bar, centre feed, recent-tags rail) are emitted
// as HTMX load-triggered hx-gets with skeleton placeholders, and that NO feed content
// is baked into the shell. Together with TestBuildShellLayout_NoUpstreamInputs this
// proves the shell is inert: nothing renders that would have required an upstream call.
func TestHomeShellRenders(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRootFromTest(t)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	h, err := NewUIHandler(nil, []byte("test-secret"), nil)
	if err != nil {
		t.Fatalf("NewUIHandler: %v", err)
	}

	user := db.User{
		Did:    "did:plc:me",
		Handle: pgtype.Text{String: "me.bsky.social", Valid: true},
		Plan:   "free",
		Theme:  "ocean",
	}
	data := buildShellLayout("did:plc:me", "sess", user, []byte("test-secret"))
	data.FeedType = "following"

	var buf strings.Builder
	if err := h.tmplHome.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("execute home layout: %v", err)
	}
	out := buf.String()

	// The three lazy regions must be present and load-triggered.
	for _, want := range []string{
		`hx-get="/feed/tabs"`,
		`hx-get="/feed/following"`,
		`hx-get="/feed/recent-tags"`,
		`hx-trigger="load"`,
		`class="skeleton`, // at least one skeleton placeholder
		`id="feed-root"`,  // still the target for feed-tab switches
	} {
		if !strings.Contains(out, want) {
			t.Errorf("home shell missing %q", want)
		}
	}

	// The shell must NOT bake in any feed outcome — neither posts, the empty state,
	// nor the failure notice. Those only appear once /feed/following responds.
	for _, unwanted := range []string{
		"post-card",
		"No posts yet",
		"feed-notice",
		"No recent tags yet",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("home shell unexpectedly contains %q (should be deferred to a lazy partial)", unwanted)
		}
	}
}

// TestFeedNoticeRenders verifies that a feed whose upstream fetch failed renders the
// "Bluesky isn't responding" notice with a Retry button wired to re-fetch RetryURL —
// NOT the misleading "no posts yet" empty state (which is reserved for a successful
// empty result, FeedError=false).
func TestFeedNoticeRenders(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRootFromTest(t)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	h, err := NewUIHandler(nil, []byte("test-secret"), nil)
	if err != nil {
		t.Fatalf("NewUIHandler: %v", err)
	}

	data := LayoutData{
		FeedType:  "following",
		FeedPage:  &feed.FeedPage{Posts: []feed.PostView{}}, // empty, but FeedError wins
		FeedError: true,
		RetryURL:  "/feed/following",
	}

	var buf strings.Builder
	if err := h.tmplHome.ExecuteTemplate(&buf, "feed", data); err != nil {
		t.Fatalf("execute feed: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"feed-notice",
		"responding",               // the reassuring title
		`hx-get="/feed/following"`, // Retry re-fetches the same feed
		`hx-target="#feed-root"`,
		"Retry",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed-notice render missing %q", want)
		}
	}
	if strings.Contains(out, "No posts yet") {
		t.Error("feed-notice render must not show the empty 'No posts yet' state")
	}
}

// TestFeedTabsDegradedRenders verifies the /feed/tabs degraded fallback: when
// getPreferences fails the tab bar shows a Following-only bar plus a quiet note,
// instead of an error or a blank bar.
func TestFeedTabsDegradedRenders(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRootFromTest(t)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	h, err := NewUIHandler(nil, []byte("test-secret"), nil)
	if err != nil {
		t.Fatalf("NewUIHandler: %v", err)
	}

	data := LayoutData{
		FeedType:     "following",
		SavedFeeds:   []feed.SavedFeed{{DisplayName: "Following", IsTimeline: true}},
		TabsDegraded: true,
	}

	var buf strings.Builder
	if err := h.tmplHome.ExecuteTemplate(&buf, "feed-tabs-inner", data); err != nil {
		t.Fatalf("execute feed-tabs-inner: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"Following",      // the fallback tab
		"feed-tabs-note", // the degraded note
		"feeds unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded tab bar missing %q", want)
		}
	}
}
