package handlers

import "testing"

func TestIsValidActor(t *testing.T) {
	cases := []struct {
		name  string
		actor string
		want  bool
	}{
		{"domain handle", "rogersherman.com", true},
		{"bsky handle", "roger.bsky.social", true},
		{"plc did", "did:plc:abcdefghijklmnopqrstuvwx", true},
		{"web did", "did:web:example.com", true},
		{"empty", "", false},
		{"garbage", "not a handle at all", false},
		{"single label", "localhost", false},
		{"slash injection", "foo/bar", false},
		{"leading dot", ".bsky.social", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidActor(tc.actor); got != tc.want {
				t.Errorf("isValidActor(%q) = %v, want %v", tc.actor, got, tc.want)
			}
		})
	}
}

func TestProfileFeedSentinelURL(t *testing.T) {
	if got := profileFeedSentinelURL("roger.bsky.social", ""); got != "" {
		t.Errorf("empty cursor should yield empty sentinel, got %q", got)
	}
	got := profileFeedSentinelURL("roger.bsky.social", "cur:123")
	want := "/profile/roger.bsky.social/feed?cursor=cur%3A123"
	if got != want {
		t.Errorf("profileFeedSentinelURL = %q, want %q", got, want)
	}
	// A dotted handle rides the path segment un-mangled (dots are path-safe).
	if got := profileFeedSentinelURL("rogersherman.com", "x"); got != "/profile/rogersherman.com/feed?cursor=x" {
		t.Errorf("dotted handle mangled: %q", got)
	}
}
