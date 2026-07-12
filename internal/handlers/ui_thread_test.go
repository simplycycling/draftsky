package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/rsherman/draftsky/internal/feed"
)

// TestThreadTemplateRenders_UnavailableGaps exercises the thread template end-to-end,
// verifying that muted/blocked ancestors and replies render as labelled "unavailable"
// gaps (not as post cards, and not silently dropped) while visible posts still render
// normally. This catches template-execution errors around the ThreadEntry union — a nil
// .Post deref on an unavailable entry, or a wrong branch — that a code-walk cannot
// (Gotcha 16).
func TestThreadTemplateRenders_UnavailableGaps(t *testing.T) {
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

	visible := func(uri, text string) feed.PostView {
		return feed.PostView{
			URI:       uri,
			CID:       "cid-" + uri,
			Author:    feed.PostAuthor{Handle: "author.bsky.social", DisplayName: "Author"},
			Text:      text,
			IndexedAt: "2026-07-10T00:00:00Z",
		}
	}

	data := ThreadPageData{
		LayoutData: LayoutData{
			User:      PageUser{DID: "did:plc:me", Handle: "me.bsky.social"},
			Theme:     "ocean",
			CSRFToken: "tok",
		},
		Thread: &feed.ThreadView{
			Ancestors: []feed.ThreadEntry{
				{Unavailable: true, Reason: "blocked"},
				{Post: ptrPV(visible("at://did:plc:author/app.bsky.feed.post/root", "the visible root"))},
			},
			Focal: visible("at://did:plc:author/app.bsky.feed.post/focal", "the focal post"),
			Replies: []feed.ThreadEntry{
				{Post: ptrPV(visible("at://did:plc:author/app.bsky.feed.post/r1", "a visible reply"))},
				{Unavailable: true, Reason: "muted"},
			},
		},
	}

	var buf strings.Builder
	if err := h.tmplThread.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("execute thread layout: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"post-card-unavailable",
		"Post unavailable (blocked)",
		"Post unavailable (muted)",
		"the visible root",
		"the focal post",
		"a visible reply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("thread render missing %q", want)
		}
	}
	// An unavailable entry must not attempt to render a post card body for it.
	if strings.Count(out, "post-card-unavailable") != 2 {
		t.Errorf("expected exactly 2 unavailable gaps, got %d", strings.Count(out, "post-card-unavailable"))
	}
}

func ptrPV(pv feed.PostView) *feed.PostView { return &pv }
