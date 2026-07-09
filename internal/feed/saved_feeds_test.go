package feed

import "testing"

func TestResolveSavedFeeds(t *testing.T) {
	const uriA = "at://did:plc:aaa/app.bsky.feed.generator/alpha"
	const uriB = "at://did:plc:bbb/app.bsky.feed.generator/beta"

	tests := []struct {
		name    string
		pinned  []pinnedFeed
		nameMap map[string]string
		// want is the ordered sequence of expected DisplayName values out.
		want []string
	}{
		{
			name:    "resolved feed is kept with its display name",
			pinned:  []pinnedFeed{{uri: uriA}},
			nameMap: map[string]string{uriA: "Alpha Feed"},
			want:    []string{"Alpha Feed"},
		},
		{
			name:    "feed absent from nameMap is skipped",
			pinned:  []pinnedFeed{{uri: uriA}},
			nameMap: map[string]string{},
			want:    []string{},
		},
		{
			name:    "feed with empty display name is skipped",
			pinned:  []pinnedFeed{{uri: uriA}},
			nameMap: map[string]string{uriA: ""},
			want:    []string{},
		},
		{
			name:    "timeline is always kept",
			pinned:  []pinnedFeed{{isTimeline: true}},
			nameMap: map[string]string{},
			want:    []string{"Following"},
		},
		{
			name: "unresolved feed skipped, order preserved for the rest",
			pinned: []pinnedFeed{
				{isTimeline: true},
				{uri: uriA},
				{uri: uriB},
			},
			// uriA is unresolvable (deleted/offline); uriB resolves.
			nameMap: map[string]string{uriB: "Beta Feed"},
			want:    []string{"Following", "Beta Feed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSavedFeeds(tt.pinned, tt.nameMap)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d feeds %+v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, sf := range got {
				if sf.DisplayName != tt.want[i] {
					t.Errorf("feed %d: DisplayName = %q, want %q", i, sf.DisplayName, tt.want[i])
				}
				// A rendered custom-feed tab must never carry a raw at:// URI as its label.
				if !sf.IsTimeline && sf.DisplayName == sf.URI {
					t.Errorf("feed %d: DisplayName equals raw URI %q — unusable tab", i, sf.URI)
				}
			}
		})
	}
}
