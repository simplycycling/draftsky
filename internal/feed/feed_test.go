package feed

import (
	"testing"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

// postView builds a minimal FeedViewPost for a given URI, optionally reposted by
// the actor with reposterDID (empty for an organic, non-repost item).
func feedViewPost(uri, reposterDID string) *appbsky.FeedDefs_FeedViewPost {
	item := &appbsky.FeedDefs_FeedViewPost{
		Post: &appbsky.FeedDefs_PostView{
			Uri: uri,
			Cid: "cid-" + uri,
			Author: &appbsky.ActorDefs_ProfileViewBasic{
				Did:    "did:plc:author",
				Handle: "author.bsky.social",
			},
		},
	}
	if reposterDID != "" {
		item.Reason = &appbsky.FeedDefs_FeedViewPost_Reason{
			FeedDefs_ReasonRepost: &appbsky.FeedDefs_ReasonRepost{
				By: &appbsky.ActorDefs_ProfileViewBasic{
					Did:    reposterDID,
					Handle: reposterDID + ".bsky.social",
				},
			},
		}
	}
	return item
}

func TestMapFeedViewPosts_Dedup(t *testing.T) {
	const uriA = "at://did:plc:author/app.bsky.feed.post/a"
	const uriB = "at://did:plc:author/app.bsky.feed.post/b"

	tests := []struct {
		name  string
		items []*appbsky.FeedDefs_FeedViewPost
		// want is the ordered sequence of (uri, reposterDID) pairs expected out.
		want [][2]string
	}{
		{
			name: "identical-key duplicate is dropped",
			items: []*appbsky.FeedDefs_FeedViewPost{
				feedViewPost(uriA, ""),
				feedViewPost(uriA, ""),
			},
			want: [][2]string{{uriA, ""}},
		},
		{
			name: "organic post and repost of same post are both kept",
			items: []*appbsky.FeedDefs_FeedViewPost{
				feedViewPost(uriA, ""),
				feedViewPost(uriA, "did:plc:reposter"),
			},
			want: [][2]string{{uriA, ""}, {uriA, "did:plc:reposter"}},
		},
		{
			name: "same repost returned twice is dropped",
			items: []*appbsky.FeedDefs_FeedViewPost{
				feedViewPost(uriA, "did:plc:reposter"),
				feedViewPost(uriA, "did:plc:reposter"),
			},
			want: [][2]string{{uriA, "did:plc:reposter"}},
		},
		{
			name: "distinct URIs are all kept in order",
			items: []*appbsky.FeedDefs_FeedViewPost{
				feedViewPost(uriB, ""),
				feedViewPost(uriA, ""),
			},
			want: [][2]string{{uriB, ""}, {uriA, ""}},
		},
		{
			name: "reposts by different actors are both kept",
			items: []*appbsky.FeedDefs_FeedViewPost{
				feedViewPost(uriA, "did:plc:reposter1"),
				feedViewPost(uriA, "did:plc:reposter2"),
			},
			want: [][2]string{{uriA, "did:plc:reposter1"}, {uriA, "did:plc:reposter2"}},
		},
		{
			name:  "nil items are skipped",
			items: []*appbsky.FeedDefs_FeedViewPost{nil, feedViewPost(uriA, "")},
			want:  [][2]string{{uriA, ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapFeedViewPosts(tt.items)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d posts, want %d", len(got), len(tt.want))
			}
			for i, pv := range got {
				wantURI, wantReposter := tt.want[i][0], tt.want[i][1]
				if pv.URI != wantURI {
					t.Errorf("post %d: URI = %q, want %q", i, pv.URI, wantURI)
				}
				var gotReposter string
				if pv.RepostedBy != nil {
					gotReposter = pv.RepostedBy.DID
				}
				if gotReposter != wantReposter {
					t.Errorf("post %d: reposter DID = %q, want %q", i, gotReposter, wantReposter)
				}
			}
		})
	}
}
