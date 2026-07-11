package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/rsherman/draftsky/internal/feed"
)

// TestProfileTemplateRenders exercises the profile page templates end-to-end: it parses
// them exactly as production does (via NewUIHandler) and executes the full "layout" for
// both an own-profile (IsMe → edit form) and another user's profile (Follow button), plus
// the "feed-more" pagination fragment. This catches template-execution errors — undefined
// field/method references, missing funcs, nil derefs — that a code-walk cannot (Gotcha 16).
func TestProfileTemplateRenders(t *testing.T) {
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

	avatar := "https://cdn.bsky.app/a.jpg"
	feedPage := &feed.FeedPage{
		Posts: []feed.PostView{{
			URI:       "at://did:plc:friend/app.bsky.feed.post/1",
			CID:       "cid1",
			Author:    feed.PostAuthor{Handle: "friend.bsky.social", DisplayName: "Friendly", Avatar: &avatar},
			Text:      "hello @roger.bsky.social #test",
			IndexedAt: "2026-07-10T00:00:00Z",
		}},
		NextCursor: "cur123",
	}

	base := LayoutData{
		User:      PageUser{DID: "did:plc:me", Handle: "me.bsky.social"},
		Theme:     "ocean",
		CSRFToken: "tok",
	}

	// Another user's profile — Follow button, bio with a mention to highlight.
	otherFollowURI := "at://did:plc:me/app.bsky.graph.follow/xyz"
	other := ProfilePageData{
		LayoutData: base,
		Actor:      "friend.bsky.social",
		Profile: &feed.Profile{
			DID:            "did:plc:friend",
			Handle:         "friend.bsky.social",
			DisplayName:    "Friendly",
			Description:    "bio mentioning @roger.bsky.social & <friends>",
			Avatar:         avatar,
			Banner:         "https://cdn.bsky.app/banner.jpg",
			FollowersCount: 1234,
			FollowsCount:   56,
			PostsCount:     789,
			IsMe:           false,
			FollowedByMe:   true,
			FollowURI:      otherFollowURI,
		},
	}
	other.FeedPage = feedPage
	other.FeedType = "profile"
	other.SentinelURL = profileFeedSentinelURL(other.Actor, feedPage.NextCursor)

	var buf strings.Builder
	if err := h.tmplProfile.ExecuteTemplate(&buf, "layout", other); err != nil {
		t.Fatalf("execute layout (other): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Following", "toggleFollow", "profile-follow-btn", "1.2K", "navigateToProfile"} {
		if !strings.Contains(out, want) {
			t.Errorf("other-profile render missing %q", want)
		}
	}

	// Own profile — edit form present, no follow button.
	me := ProfilePageData{
		LayoutData: base,
		Actor:      "me.bsky.social",
		Profile: &feed.Profile{
			DID:         "did:plc:me",
			Handle:      "me.bsky.social",
			DisplayName: "Me",
			Description: "my bio",
			IsMe:        true,
		},
	}
	me.FeedPage = &feed.FeedPage{Posts: []feed.PostView{}}
	me.FeedType = "profile"

	buf.Reset()
	if err := h.tmplProfile.ExecuteTemplate(&buf, "layout", me); err != nil {
		t.Fatalf("execute layout (me): %v", err)
	}
	out = buf.String()
	if !strings.Contains(out, "profile-edit-form") || !strings.Contains(out, "showProfileEdit") {
		t.Error("own-profile render missing edit form")
	}
	if strings.Contains(out, "toggleFollow") {
		t.Error("own-profile render must not include the follow button")
	}
	if !strings.Contains(out, "No posts yet") {
		t.Error("own-profile empty feed should show the empty state")
	}

	// Pagination fragment.
	buf.Reset()
	if err := h.tmplProfile.ExecuteTemplate(&buf, "feed-more", other); err != nil {
		t.Fatalf("execute feed-more: %v", err)
	}
	if !strings.Contains(buf.String(), "feed-sentinel") {
		t.Error("feed-more should include the scroll sentinel when a cursor is present")
	}
}
