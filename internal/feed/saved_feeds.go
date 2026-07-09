package feed

import (
	"context"
	"fmt"

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

	type orderedItem struct {
		value      string
		isTimeline bool
	}
	var pinned []orderedItem
	var feedURIs []string

	for _, item := range v2.Items {
		if item == nil || !item.Pinned {
			continue
		}
		switch item.Type {
		case "timeline":
			pinned = append(pinned, orderedItem{isTimeline: true})
		case "feed":
			pinned = append(pinned, orderedItem{value: item.Value})
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
		for _, g := range gens.Feeds {
			if g != nil {
				nameMap[g.Uri] = g.DisplayName
			}
		}
	}

	result := make([]SavedFeed, 0, len(pinned))
	for _, p := range pinned {
		if p.isTimeline {
			result = append(result, SavedFeed{DisplayName: "Following", IsTimeline: true})
		} else {
			name := nameMap[p.value]
			if name == "" {
				name = p.value // fallback to URI if name resolution failed
			}
			result = append(result, SavedFeed{URI: p.value, DisplayName: name})
		}
	}
	return result, nil
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
