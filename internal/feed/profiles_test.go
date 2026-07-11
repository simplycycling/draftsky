package feed

import (
	"errors"
	"testing"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
)

func i64ptr(n int64) *int64 { return &n }

func TestMapProfile(t *testing.T) {
	following := "at://did:plc:me/app.bsky.graph.follow/abc"
	out := &appbsky.ActorDefs_ProfileViewDetailed{
		Did:            "did:plc:friend",
		Handle:         "friend.bsky.social",
		DisplayName:    strptr("Friendly"),
		Description:    strptr("hello @roger.bsky.social"),
		Avatar:         strptr("https://cdn.bsky.app/avatar.jpg"),
		Banner:         strptr("https://cdn.bsky.app/banner.jpg"),
		FollowersCount: i64ptr(42),
		FollowsCount:   i64ptr(7),
		PostsCount:     i64ptr(100),
		Viewer:         &appbsky.ActorDefs_ViewerState{Following: &following},
	}

	p := mapProfile(out, "did:plc:me")

	if p.DID != "did:plc:friend" || p.Handle != "friend.bsky.social" {
		t.Fatalf("identity mismatch: %+v", p)
	}
	if p.DisplayName != "Friendly" || p.Description != "hello @roger.bsky.social" {
		t.Errorf("text fields mismatch: %+v", p)
	}
	if p.Avatar == "" || p.Banner == "" {
		t.Errorf("avatar/banner not mapped: %+v", p)
	}
	if p.FollowersCount != 42 || p.FollowsCount != 7 || p.PostsCount != 100 {
		t.Errorf("counts mismatch: %+v", p)
	}
	if p.IsMe {
		t.Errorf("IsMe should be false when session DID differs from profile DID")
	}
	if !p.FollowedByMe || p.FollowURI != following {
		t.Errorf("viewer following state not carried: %+v", p)
	}
}

func TestMapProfile_IsMe(t *testing.T) {
	out := &appbsky.ActorDefs_ProfileViewDetailed{
		Did:    "did:plc:me",
		Handle: "me.bsky.social",
	}
	p := mapProfile(out, "did:plc:me")
	if !p.IsMe {
		t.Errorf("IsMe should be true when profile DID == session DID")
	}
	// Nil optional fields must map to zero values, never panic.
	if p.DisplayName != "" || p.FollowersCount != 0 || p.FollowedByMe {
		t.Errorf("unexpected non-zero fields for sparse profile: %+v", p)
	}
}

// TestApplyProfileEdits_PreservesBlobs is the data-loss guard: the get-then-put edit
// must never drop the user's avatar or banner blobs. This pins that a fetched record
// with avatar/banner set keeps both after only display name + bio are edited.
func TestApplyProfileEdits_PreservesBlobs(t *testing.T) {
	avatar := &lexutil.LexBlob{}
	banner := &lexutil.LexBlob{}
	createdAt := "2023-01-01T00:00:00Z"
	pinned := "at://did:plc:me/app.bsky.feed.post/pinned"
	existing := &appbsky.ActorProfile{
		Avatar:      avatar,
		Banner:      banner,
		CreatedAt:   &createdAt,
		DisplayName: strptr("Old Name"),
		Description: strptr("old bio"),
		PinnedPost:  nil,
	}
	_ = pinned

	got := applyProfileEdits(existing, "New Name", "new bio")

	if got.Avatar != avatar {
		t.Errorf("avatar blob was not preserved: got %v want %v", got.Avatar, avatar)
	}
	if got.Banner != banner {
		t.Errorf("banner blob was not preserved: got %v want %v", got.Banner, banner)
	}
	if got.CreatedAt == nil || *got.CreatedAt != createdAt {
		t.Errorf("createdAt not preserved: %v", got.CreatedAt)
	}
	if got.DisplayName == nil || *got.DisplayName != "New Name" {
		t.Errorf("display name not updated: %v", got.DisplayName)
	}
	if got.Description == nil || *got.Description != "new bio" {
		t.Errorf("description not updated: %v", got.Description)
	}
}

func TestApplyProfileEdits_ClearsOnEmpty(t *testing.T) {
	existing := &appbsky.ActorProfile{
		DisplayName: strptr("Old Name"),
		Description: strptr("old bio"),
	}
	got := applyProfileEdits(existing, "", "")
	if got.DisplayName != nil {
		t.Errorf("empty display name should clear the field, got %v", *got.DisplayName)
	}
	if got.Description != nil {
		t.Errorf("empty description should clear the field, got %v", *got.Description)
	}
}

func TestApplyProfileEdits_NilExisting(t *testing.T) {
	// A first-ever edit (no self record yet) starts from nil and must not panic.
	got := applyProfileEdits(nil, "Name", "bio")
	if got == nil || got.DisplayName == nil || *got.DisplayName != "Name" {
		t.Fatalf("nil existing not handled: %+v", got)
	}
}

func TestIsRecordNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("XRPC ERROR 400: RecordNotFound: Could not locate record"), true},
		{errors.New("could not locate record: at://..."), true},
		{errors.New("record not found"), true},
		{errors.New("dial tcp: connection refused"), false},
		{errors.New("XRPC ERROR 401: ExpiredToken"), false},
	}
	for _, tc := range cases {
		if got := isRecordNotFound(tc.err); got != tc.want {
			t.Errorf("isRecordNotFound(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
