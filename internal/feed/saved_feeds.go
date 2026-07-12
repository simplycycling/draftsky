package feed

import (
	"context"
	"fmt"
	"log/slog"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/atclient"
)

// SavedFeed represents a pinned feed from the user's Bluesky preferences.
// IsTimeline is true for the built-in Following timeline ("timeline" type);
// false for custom algorithm feeds ("feed" type, which carry an AT URI).
type SavedFeed struct {
	URI         string // AT URI for custom feeds; "" for the timeline
	DisplayName string
	IsTimeline  bool
}

// Preferences bundles the pieces of app.bsky.actor.getPreferences DraftSky consumes,
// fetched in a single call so a page render pays one round-trip for both the saved-feeds
// tab bar and muted-word filtering (see GetPreferences).
type Preferences struct {
	SavedFeeds []SavedFeed
	MutedWords []MutedWord
}

// GetPreferences fetches the user's Bluesky preferences ONCE and returns both the resolved
// saved feeds (for the tab bar) and the muted words (for feed content filtering). This is
// the page-render consumer: buildLayoutBase calls it so a single getPreferences serves both
// the chrome and the Following feed's muted-word filter. Consumers that only need muted
// words (feed partials, JSON API) should call GetMutedWords instead, which skips the extra
// getFeedGenerators round-trip that saved-feed name resolution requires.
//
// On failure the caller degrades gracefully; see buildLayoutBase.
func (c *Client) GetPreferences(ctx context.Context, did, sessionID string) (*Preferences, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	prefs, err := appbsky.ActorGetPreferences(ctx, apiClient)
	if err != nil {
		return nil, fmt.Errorf("getPreferences: %w", err)
	}

	saved, err := c.resolveSavedFeedsFromPrefs(ctx, apiClient, prefs)
	if err != nil {
		return nil, err
	}
	return &Preferences{
		SavedFeeds: saved,
		MutedWords: mutedWordsFromPrefs(prefs),
	}, nil
}

// GetMutedWords fetches only the user's muted words in a single getPreferences call,
// skipping saved-feed name resolution. Used by feed fetches (partials + JSON API) that need
// muted-word filtering but not the tab bar.
func (c *Client) GetMutedWords(ctx context.Context, did, sessionID string) ([]MutedWord, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}
	prefs, err := appbsky.ActorGetPreferences(ctx, apiClient)
	if err != nil {
		return nil, fmt.Errorf("getPreferences: %w", err)
	}
	return mutedWordsFromPrefs(prefs), nil
}

// mutedWordsFromPrefs extracts the muted-word list from a preferences response. Multiple
// mutedWordsPref entries are unusual but all are flattened; entries with an empty value are
// dropped.
func mutedWordsFromPrefs(prefs *appbsky.ActorGetPreferences_Output) []MutedWord {
	var out []MutedWord
	for _, pref := range prefs.Preferences {
		if pref.ActorDefs_MutedWordsPref == nil {
			continue
		}
		for _, it := range pref.ActorDefs_MutedWordsPref.Items {
			if it == nil || it.Value == "" {
				continue
			}
			mw := MutedWord{Value: it.Value}
			for _, t := range it.Targets {
				if t != nil {
					mw.Targets = append(mw.Targets, *t)
				}
			}
			if it.ActorTarget != nil {
				mw.ActorTarget = *it.ActorTarget
			}
			if it.ExpiresAt != nil {
				mw.ExpiresAt = *it.ExpiresAt
			}
			out = append(out, mw)
		}
	}
	return out
}

// resolveSavedFeedsFromPrefs extracts pinned saved feeds from a preferences response and
// resolves custom-feed display names via getFeedGenerators. Returns a single Following-only
// entry when there are no usable pinned feeds. Split out of GetPreferences so the parsing
// stays testable and the getFeedGenerators round-trip is confined to the saved-feeds path.
func (c *Client) resolveSavedFeedsFromPrefs(ctx context.Context, apiClient *atclient.APIClient, prefs *appbsky.ActorGetPreferences_Output) ([]SavedFeed, error) {
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
func (c *Client) GetCustomFeed(ctx context.Context, did, sessionID, feedURI, cursor string, limit int, mutedWords []MutedWord) (*FeedPage, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	out, err := appbsky.FeedGetFeed(ctx, apiClient, cursor, feedURI, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("getFeed %q: %w", feedURI, err)
	}

	posts := mapFeedViewPosts(out.Feed, mutedWords)

	var nextCursor string
	if out.Cursor != nil {
		nextCursor = *out.Cursor
	}
	return &FeedPage{Posts: posts, NextCursor: nextCursor}, nil
}
