package handlers

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rsherman/draftsky/internal/feed"
)

// repoRootFromTest resolves the module root relative to this test file so that
// NewUIHandler's relative template paths ("templates/...") resolve regardless of
// the package the `go test` process is launched from.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestNotificationsTemplateRenders exercises the notifications templates end-to-end:
// it parses them exactly as production does (via NewUIHandler) and executes both the
// full "layout" and the "notifications-more" pagination fragment against a mix of
// reasons (unread reply with a snippet+avatar, a non-clickable follow, an
// avatar-less like). This catches parse errors AND template-execution errors —
// undefined field/method references, missing funcs — that a code-walk cannot.
func TestNotificationsTemplateRenders(t *testing.T) {
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

	displayName := "Alice"
	avatar := "https://example.com/a.jpg"
	data := NotificationsPageData{
		LayoutData: LayoutData{
			User:        PageUser{DID: "did:plc:me", Handle: "me.bsky.social"},
			Theme:       "ocean",
			CSRFToken:   "tok",
			UnreadCount: 3,
			SentinelURL: "/notifications?cursor=abc",
		},
		Notifications: []feed.Notification{
			{
				URI:       "at://did:plc:alice/app.bsky.feed.post/1",
				Reason:    "reply",
				Author:    feed.PostAuthor{Handle: "alice", DisplayName: displayName, Avatar: &avatar},
				IsRead:    false,
				IndexedAt: "2026-07-10T00:00:00Z",
				Snippet:   "great <post> & thoughts",
			},
			{
				URI:       "at://did:plc:bob/app.bsky.graph.follow/1",
				Reason:    "follow", // non-clickable
				Author:    feed.PostAuthor{Handle: "bob"},
				IsRead:    true,
				IndexedAt: "2026-07-10T00:00:00Z",
			},
			{
				URI:              "at://did:plc:carol/app.bsky.feed.like/1",
				Reason:           "like",
				ReasonSubjectURI: "at://did:plc:me/app.bsky.feed.post/x",
				Author:           feed.PostAuthor{Handle: "carol"}, // no display name, no avatar
				IsRead:           true,
				IndexedAt:        "2026-07-10T00:00:00Z",
			},
		},
	}

	if err := h.tmplNotifications.ExecuteTemplate(io.Discard, "layout", data); err != nil {
		t.Fatalf("execute layout: %v", err)
	}
	if err := h.tmplNotifications.ExecuteTemplate(io.Discard, "notifications-more", data); err != nil {
		t.Fatalf("execute notifications-more: %v", err)
	}
}
