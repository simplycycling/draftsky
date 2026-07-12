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
			got := mapFeedViewPosts(tt.items, nil)
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

func strptr(s string) *string { return &s }

// videoView builds an app.bsky.embed.video#view with a 16:9 aspect ratio.
func videoView() *appbsky.EmbedVideo_View {
	return &appbsky.EmbedVideo_View{
		Cid:         "bafyvideo",
		Playlist:    "https://video.bsky.app/watch/did:plc:x/bafyvideo/playlist.m3u8",
		Thumbnail:   strptr("https://video.bsky.app/watch/did:plc:x/bafyvideo/thumbnail.jpg"),
		Alt:         strptr("a clip"),
		AspectRatio: &appbsky.EmbedDefs_AspectRatio{Width: 16, Height: 9},
	}
}

func TestPostViewFromBsky_Video_DirectEmbed(t *testing.T) {
	pv := postViewFromBsky(&appbsky.FeedDefs_PostView{
		Uri: "at://did:plc:x/app.bsky.feed.post/1",
		Cid: "cid1",
		Embed: &appbsky.FeedDefs_PostView_Embed{
			EmbedVideo_View: videoView(),
		},
	})
	if pv.Video == nil {
		t.Fatal("Video is nil, want populated")
	}
	if pv.Video.Playlist != "https://video.bsky.app/watch/did:plc:x/bafyvideo/playlist.m3u8" {
		t.Errorf("Playlist = %q", pv.Video.Playlist)
	}
	if pv.Video.Thumbnail == "" || pv.Video.Alt != "a clip" {
		t.Errorf("Thumbnail=%q Alt=%q", pv.Video.Thumbnail, pv.Video.Alt)
	}
	if got := pv.Video.AspectRatio; got < 1.77 || got > 1.78 {
		t.Errorf("AspectRatio = %v, want ~1.777", got)
	}
}

func TestPostViewFromBsky_Video_RecordWithMedia(t *testing.T) {
	pv := postViewFromBsky(&appbsky.FeedDefs_PostView{
		Uri: "at://did:plc:x/app.bsky.feed.post/2",
		Cid: "cid2",
		Embed: &appbsky.FeedDefs_PostView_Embed{
			EmbedRecordWithMedia_View: &appbsky.EmbedRecordWithMedia_View{
				Media: &appbsky.EmbedRecordWithMedia_View_Media{
					EmbedVideo_View: videoView(),
				},
			},
		},
	})
	if pv.Video == nil || pv.Video.Playlist == "" {
		t.Fatal("Video not mapped from recordWithMedia media half")
	}
}

func TestMapEmbedRecordView_Video_ThumbnailOnly(t *testing.T) {
	qp := mapEmbedRecordView(&appbsky.EmbedRecord_View_Record{
		EmbedRecord_ViewRecord: &appbsky.EmbedRecord_ViewRecord{
			Uri: "at://did:plc:x/app.bsky.feed.post/q",
			Author: &appbsky.ActorDefs_ProfileViewBasic{
				Did:    "did:plc:x",
				Handle: "quoted.bsky.social",
			},
			Embeds: []*appbsky.EmbedRecord_ViewRecord_Embeds_Elem{
				{EmbedVideo_View: videoView()},
			},
		},
	})
	if qp == nil || qp.Video == nil {
		t.Fatal("quoted Video is nil, want populated")
	}
	if qp.Video.Playlist != "" {
		t.Errorf("quoted Video.Playlist = %q, want empty (thumbnail-only)", qp.Video.Playlist)
	}
	if qp.Video.Thumbnail == "" {
		t.Error("quoted Video.Thumbnail is empty, want populated")
	}
}

func TestMapEmbedVideoView_UnknownAspectRatio(t *testing.T) {
	// Height 0 (or nil ratio) must yield AspectRatio 0, never a divide-by-zero.
	got := mapEmbedVideoView(&appbsky.EmbedVideo_View{
		Playlist:    "https://video.bsky.app/x.m3u8",
		AspectRatio: &appbsky.EmbedDefs_AspectRatio{Width: 16, Height: 0},
	})
	if got.AspectRatio != 0 {
		t.Errorf("AspectRatio = %v, want 0 for height=0", got.AspectRatio)
	}
	if mapEmbedVideoView(nil) != nil {
		t.Error("mapEmbedVideoView(nil) should return nil")
	}
}
