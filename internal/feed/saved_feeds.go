package feed

import (
	"context"
	"fmt"
	"log/slog"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

// SavedFeed represents a pinned feed from the user's Bluesky preferences.
// IsTimeline is true for the built-in Following timeline ("timeline" type);
// false for custom algorithm feeds ("feed" type, which carry an AT URI).
type SavedFeed struct {
	URI         string // AT URI for custom feeds; "" for the timeline
	DisplayName string
	IsTimeline  bool
}

// GetSavedFeeds fetches the user's pinned saved feeds from Bluesky preferences,
// resolves display names for custom feeds, and returns them in preference order.
// Only "feed" and "timeline" types are included; "list" is skipped.
// On any failure, returns a single Following-only entry so the caller can degrade
// gracefully without breaking the page.
func (c *Client) GetSavedFeeds(ctx context.Context, did, sessionID string) ([]SavedFeed, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	prefs, err := appbsky.ActorGetPreferences(ctx, apiClient)
	if err != nil {
		return nil, fmt.Errorf("getPreferences: %w", err)
	}

	// Use savedFeedsPrefV2 — if multiple entries exist, keep the last one.
	var v2 *appbsky.ActorDefs_SavedFeedsPrefV2
	for _, pref := range prefs.Preferences {
		if pref.ActorDefs_SavedFeedsPrefV2 != nil {
			v2 = pref.ActorDefs_SavedFeedsPrefV2
		}
	}

	if v2 == nil || len(v2.Items) == 0 {
		return []SavedFeed{{DisplayName: "Following", IsTimeline: true}}, nil
	}

	var pinned []pinnedFeed
	var feedURIs []string

	for _, item := range v2.Items {
		if item == nil || !item.Pinned {
			continue
		}
		switch item.Type {
		case "timeline":
			pinned = append(pinned, pinnedFeed{isTimeline: true})
		case "feed":
			pinned = append(pinned, pinnedFeed{uri: item.Value})
			feedURIs = append(feedURIs, item.Value)
			// "list" is skipped
		}
	}

	if len(pinned) == 0 {
		return []SavedFeed{{DisplayName: "Following", IsTimeline: true}}, nil
	}

	// Resolve display names for custom feed URIs via getFeedGenerators.
	nameMap := make(map[string]string, len(feedURIs))
	if len(feedURIs) > 0 {
		gens, err := appbsky.FeedGetFeedGenerators(ctx, apiClient, feedURIs)
		if err != nil {
			return nil, fmt.Errorf("getFeedGenerators: %w", err)
		}
		// getFeedGenerators omits feeds that are deleted or whose generator
		// service is offline, so a shorter response than the request is expected
		// for those. But it should never silently drop *valid* feeds — log a
		// mismatch so we can tell an ordinary "feed gone" case apart from the
		// batch call failing partway and dropping resolvable feeds too.
		if len(gens.Feeds) != len(feedURIs) {
			slog.Warn("getFeedGenerators returned fewer feeds than requested",
				"requested", len(feedURIs), "resolved", len(gens.Feeds))
		}
		for _, g := range gens.Feeds {
			if g != nil {
				nameMap[g.Uri] = g.DisplayName
			}
		}
	}

	return resolveSavedFeeds(pinned, nameMap), nil
}

// pinnedFeed is a pinned saved feed extracted from preferences, before
// display-name resolution. isTimeline marks the built-in Following timeline;
// uri carries the AT URI for custom algorithm feeds.
type pinnedFeed struct {
	uri        string
	isTimeline bool
}

// resolveSavedFeeds builds the ordered SavedFeed slice from the extracted pinned
// feeds and the display-name map returned by getFeedGenerators. The timeline is
// always kept. A custom feed whose URI is absent from nameMap, or resolves to an
// empty display name, is skipped entirely rather than rendered as a tab labelled
// with its raw at:// URI — such feeds are deleted or their generator service is
// offline, so there is nothing usable to show. A WARN is logged per skip so we
// can see how often it happens. Preference order is preserved.
func resolveSavedFeeds(pinned []pinnedFeed, nameMap map[string]string) []SavedFeed {
	result := make([]SavedFeed, 0, len(pinned))
	for _, p := range pinned {
		if p.isTimeline {
			result = append(result, SavedFeed{DisplayName: "Following", IsTimeline: true})
			continue
		}
		name := nameMap[p.uri]
		if name == "" {
			slog.Warn("saved feed unresolved; skipping from tab bar", "uri", p.uri)
			continue
		}
		result = append(result, SavedFeed{URI: p.uri, DisplayName: name})
	}
	return result
}

// GetCustomFeed fetches a page of posts from a Bluesky algorithm feed via
// app.bsky.feed.getFeed. cursor is the opaque pagination cursor from the previous
// response; pass "" for the first page.
func (c *Client) GetCustomFeed(ctx context.Context, did, sessionID, feedURI, cursor string, limit int) (*FeedPage, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	out, err := appbsky.FeedGetFeed(ctx, apiClient, cursor, feedURI, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("getFeed %q: %w", feedURI, err)
	}

	posts := mapFeedViewPosts(out.Feed)

	var nextCursor string
	if out.Cursor != nil {
		nextCursor = *out.Cursor
	}
	return &FeedPage{Posts: posts, NextCursor: nextCursor}, nil
}
