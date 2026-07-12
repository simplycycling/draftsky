package feed

import (
	"testing"
	"time"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
)

func boolptr(b bool) *bool { return &b }

// viewer builders for the moderation flags on an author's ActorDefs_ViewerState.
func mutedViewer() *appbsky.ActorDefs_ViewerState {
	return &appbsky.ActorDefs_ViewerState{Muted: boolptr(true)}
}
func blockedByViewer() *appbsky.ActorDefs_ViewerState {
	return &appbsky.ActorDefs_ViewerState{BlockedBy: boolptr(true)}
}
func blockingViewer() *appbsky.ActorDefs_ViewerState {
	return &appbsky.ActorDefs_ViewerState{Blocking: strptr("at://did:plc:me/app.bsky.graph.block/1")}
}
func followingViewer() *appbsky.ActorDefs_ViewerState {
	return &appbsky.ActorDefs_ViewerState{Following: strptr("at://did:plc:me/app.bsky.graph.follow/1")}
}

func TestAuthorHidden(t *testing.T) {
	tests := []struct {
		name string
		v    *appbsky.ActorDefs_ViewerState
		want bool
	}{
		{"nil viewer", nil, false},
		{"empty viewer", &appbsky.ActorDefs_ViewerState{}, false},
		{"muted", mutedViewer(), true},
		{"muted=false", &appbsky.ActorDefs_ViewerState{Muted: boolptr(false)}, false},
		{"muted by list", &appbsky.ActorDefs_ViewerState{MutedByList: &appbsky.GraphDefs_ListViewBasic{}}, true},
		{"blockedBy", blockedByViewer(), true},
		{"blocking", blockingViewer(), true},
		{"blocking empty string", &appbsky.ActorDefs_ViewerState{Blocking: strptr("")}, false},
		{"blocking by list", &appbsky.ActorDefs_ViewerState{BlockingByList: &appbsky.GraphDefs_ListViewBasic{}}, true},
		{"following only", followingViewer(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorHidden(tt.v); got != tt.want {
				t.Errorf("authorHidden = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesMutedWords(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour).Format(time.RFC3339)
	past := now.Add(-24 * time.Hour).Format(time.RFC3339)

	content := func(v string) MutedWord { return MutedWord{Value: v, Targets: []string{"content"}} }
	tag := func(v string) MutedWord { return MutedWord{Value: v, Targets: []string{"tag"}} }

	tests := []struct {
		name      string
		words     []MutedWord
		text      string
		hashtags  []string
		following bool
		want      bool
	}{
		{"no words", nil, "anything", nil, false, false},
		{"tag hit", []MutedWord{tag("njdevils")}, "go team", []string{"njdevils"}, false, true},
		{"tag hit hash-insensitive value", []MutedWord{tag("#njdevils")}, "go", []string{"njdevils"}, false, true},
		{"tag hit case-insensitive", []MutedWord{tag("NJDevils")}, "go", []string{"njdevils"}, false, true},
		{"tag miss", []MutedWord{tag("nhl")}, "go", []string{"njdevils"}, false, false},
		{"content whole-word hit", []MutedWord{content("spoilers")}, "no SPOILERS please", nil, false, true},
		{"content case-insensitive", []MutedWord{content("Trade")}, "big trade news", nil, false, true},
		{"content near-miss substring: cat != catalogue", []MutedWord{content("cat")}, "check the catalogue", nil, false, false},
		{"content whole word cat hits standalone cat", []MutedWord{content("cat")}, "my cat sleeps", nil, false, true},
		{"content word with trailing punctuation", []MutedWord{content("trade")}, "it's a trade!", nil, false, true},
		{"content target does not match hashtag-only", []MutedWord{content("njdevils")}, "go", []string{"njdevils"}, false, false},
		{"tag target does not match body text", []MutedWord{tag("trade")}, "big trade news", nil, false, false},
		{"phrase substring match", []MutedWord{content("election results")}, "the ELECTION RESULTS are in", nil, false, true},
		{"phrase miss", []MutedWord{content("election results")}, "election night", nil, false, false},
		{"expired word skipped", []MutedWord{{Value: "trade", Targets: []string{"content"}, ExpiresAt: past}}, "a trade", nil, false, false},
		{"future expiry still active", []MutedWord{{Value: "trade", Targets: []string{"content"}, ExpiresAt: future}}, "a trade", nil, false, true},
		{"exclude-following spares followed author", []MutedWord{{Value: "trade", Targets: []string{"content"}, ActorTarget: "exclude-following"}}, "a trade", nil, true, false},
		{"exclude-following still hits non-followed", []MutedWord{{Value: "trade", Targets: []string{"content"}, ActorTarget: "exclude-following"}}, "a trade", nil, false, true},
		{"actorTarget all hits followed", []MutedWord{{Value: "trade", Targets: []string{"content"}, ActorTarget: "all"}}, "a trade", nil, true, true},
		{"empty value skipped", []MutedWord{{Value: "  ", Targets: []string{"content"}}}, "anything", nil, false, false},
		{"content+tag word hits either", []MutedWord{{Value: "njdevils", Targets: []string{"content", "tag"}}}, "go", []string{"njdevils"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesMutedWords(tt.words, tt.text, tt.hashtags, tt.following, now); got != tt.want {
				t.Errorf("matchesMutedWords = %v, want %v", got, tt.want)
			}
		})
	}
}

// feedItem builds a FeedViewPost with the given author viewer state and post text (the
// text is stored on the record so postViewFromBsky and muted-word matching see it).
func feedItem(uri, text string, authorViewer *appbsky.ActorDefs_ViewerState) *appbsky.FeedDefs_FeedViewPost {
	return &appbsky.FeedDefs_FeedViewPost{
		Post: &appbsky.FeedDefs_PostView{
			Uri: uri,
			Cid: "cid-" + uri,
			Author: &appbsky.ActorDefs_ProfileViewBasic{
				Did:    "did:plc:author",
				Handle: "author.bsky.social",
				Viewer: authorViewer,
			},
			Record: &lexutil.LexiconTypeDecoder{Val: &appbsky.FeedPost{Text: text}},
		},
	}
}

// repostItem wraps feedItem with a repost reason whose reposter carries reposterViewer.
func repostItem(uri, text string, authorViewer, reposterViewer *appbsky.ActorDefs_ViewerState) *appbsky.FeedDefs_FeedViewPost {
	item := feedItem(uri, text, authorViewer)
	item.Reason = &appbsky.FeedDefs_FeedViewPost_Reason{
		FeedDefs_ReasonRepost: &appbsky.FeedDefs_ReasonRepost{
			By: &appbsky.ActorDefs_ProfileViewBasic{
				Did:    "did:plc:reposter",
				Handle: "reposter.bsky.social",
				Viewer: reposterViewer,
			},
		},
	}
	return item
}

func TestMapFeedViewPosts_Moderation(t *testing.T) {
	const uri = "at://did:plc:author/app.bsky.feed.post/x"

	uris := func(posts []PostView) []string {
		out := make([]string, len(posts))
		for i, p := range posts {
			out[i] = p.URI
		}
		return out
	}

	t.Run("muted author dropped", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "hi", mutedViewer())}, nil)
		if len(got) != 0 {
			t.Errorf("muted author not dropped: %v", uris(got))
		}
	})
	t.Run("blockedBy author dropped", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "hi", blockedByViewer())}, nil)
		if len(got) != 0 {
			t.Errorf("blockedBy author not dropped: %v", uris(got))
		}
	})
	t.Run("blocking author dropped", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "hi", blockingViewer())}, nil)
		if len(got) != 0 {
			t.Errorf("blocking author not dropped: %v", uris(got))
		}
	})
	t.Run("clean author kept", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "hi", nil)}, nil)
		if len(got) != 1 {
			t.Errorf("clean author dropped: %v", uris(got))
		}
	})
	t.Run("repost by muted account dropped", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{repostItem(uri, "hi", nil, mutedViewer())}, nil)
		if len(got) != 0 {
			t.Errorf("repost by muted account not dropped: %v", uris(got))
		}
	})
	t.Run("repost by clean account of clean author kept", func(t *testing.T) {
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{repostItem(uri, "hi", nil, followingViewer())}, nil)
		if len(got) != 1 {
			t.Errorf("clean repost dropped: %v", uris(got))
		}
	})
	t.Run("muted word in text dropped", func(t *testing.T) {
		words := []MutedWord{{Value: "spoilers", Targets: []string{"content"}}}
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "big spoilers here", nil)}, words)
		if len(got) != 0 {
			t.Errorf("muted-word post not dropped: %v", uris(got))
		}
	})
	t.Run("muted word inline hashtag dropped", func(t *testing.T) {
		words := []MutedWord{{Value: "njdevils", Targets: []string{"tag"}}}
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "go team #njdevils", nil)}, words)
		if len(got) != 0 {
			t.Errorf("muted-tag post not dropped: %v", uris(got))
		}
	})
	t.Run("muted word spares followed author under exclude-following", func(t *testing.T) {
		words := []MutedWord{{Value: "spoilers", Targets: []string{"content"}, ActorTarget: "exclude-following"}}
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "big spoilers", followingViewer())}, words)
		if len(got) != 1 {
			t.Errorf("exclude-following muted word wrongly dropped a followed author's post: %v", uris(got))
		}
	})
	t.Run("non-matching post with muted words kept", func(t *testing.T) {
		words := []MutedWord{{Value: "spoilers", Targets: []string{"content"}}}
		got := mapFeedViewPosts([]*appbsky.FeedDefs_FeedViewPost{feedItem(uri, "totally fine post", nil)}, words)
		if len(got) != 1 {
			t.Errorf("clean post wrongly dropped: %v", uris(got))
		}
	})
}
