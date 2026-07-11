package feed

import (
	"context"
	"fmt"
	"strings"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	appbsky "github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
)

// profileRecordCollection and profileRecordRkey identify the single self-profile
// record every account holds at app.bsky.actor.profile/self.
const (
	profileRecordCollection = "app.bsky.actor.profile"
	profileRecordRkey       = "self"
)

// Profile is the clean representation of a Bluesky actor profile. No indigo types
// leak out of this struct.
type Profile struct {
	DID            string `json:"did"`
	Handle         string `json:"handle"`
	DisplayName    string `json:"display_name,omitempty"`
	Description    string `json:"description,omitempty"`
	Avatar         string `json:"avatar,omitempty"`
	Banner         string `json:"banner,omitempty"`
	FollowersCount int64  `json:"followers_count"`
	FollowsCount   int64  `json:"follows_count"`
	PostsCount     int64  `json:"posts_count"`
	// IsMe is true when the resolved profile DID matches the session's DID.
	IsMe bool `json:"is_me"`
	// FollowedByMe is true when the viewer already follows this actor; FollowURI is
	// then the AT URI of the viewer's follow record (needed to unfollow). Populated
	// from viewer.following. Both are zero for the viewer's own profile.
	FollowedByMe bool   `json:"followed_by_me"`
	FollowURI    string `json:"follow_uri,omitempty"`
}

// mapProfile converts an indigo ProfileViewDetailed to a clean Profile. sessionDID is
// the DID of the requesting user, used to set IsMe. Extracted as a pure function so the
// mapping (including the IsMe and viewer-state logic) is unit-testable without a live PDS.
func mapProfile(out *appbsky.ActorDefs_ProfileViewDetailed, sessionDID string) *Profile {
	p := &Profile{
		DID:    out.Did,
		Handle: out.Handle,
		IsMe:   out.Did == sessionDID,
	}
	if out.DisplayName != nil {
		p.DisplayName = *out.DisplayName
	}
	if out.Description != nil {
		p.Description = *out.Description
	}
	if out.Avatar != nil {
		p.Avatar = *out.Avatar
	}
	if out.Banner != nil {
		p.Banner = *out.Banner
	}
	if out.FollowersCount != nil {
		p.FollowersCount = *out.FollowersCount
	}
	if out.FollowsCount != nil {
		p.FollowsCount = *out.FollowsCount
	}
	if out.PostsCount != nil {
		p.PostsCount = *out.PostsCount
	}
	if out.Viewer != nil && out.Viewer.Following != nil {
		p.FollowedByMe = true
		p.FollowURI = *out.Viewer.Following
	}
	return p
}

// GetProfile fetches an actor's profile via app.bsky.actor.getProfile. actor accepts
// either a handle or a DID. All requests run under the user's OAuth session so viewer
// state (following/followedBy) is populated.
func (c *Client) GetProfile(ctx context.Context, did, sessionID, actor string) (*Profile, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	out, err := appbsky.ActorGetProfile(ctx, apiClient, actor)
	if err != nil {
		return nil, fmt.Errorf("getProfile(%q): %w", actor, err)
	}

	return mapProfile(out, did), nil
}

// GetAuthorFeed returns a page of an actor's posts via app.bsky.feed.getAuthorFeed.
// The items are FeedViewPosts, so mapFeedViewPosts is reused wholesale — dedup,
// embeds, and reply/repost context all come for free. cursor is the opaque
// pagination cursor from the previous response; pass "" for the first page.
func (c *Client) GetAuthorFeed(ctx context.Context, did, sessionID, actor, cursor string, limit int) (*FeedPage, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	// filter="" → default (posts + reposts, no replies); includePins=false.
	out, err := appbsky.FeedGetAuthorFeed(ctx, apiClient, actor, cursor, "", false, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("getAuthorFeed(%q): %w", actor, err)
	}

	posts := mapFeedViewPosts(out.Feed)

	var nextCursor string
	if out.Cursor != nil {
		nextCursor = *out.Cursor
	}

	return &FeedPage{Posts: posts, NextCursor: nextCursor}, nil
}

// applyProfileEdits mutates a fetched profile record in place, overwriting ONLY the
// display name and description while leaving every other field — crucially the avatar
// and banner blobs — untouched. An empty string clears the corresponding field (nil
// pointer). This is the heart of the get-then-put flow: a naive "create a fresh
// ActorProfile with just displayName/description" would drop the user's avatar and
// banner (they are blob refs, not re-derivable client-side), silently deleting them.
// Extracted as a pure function so the preservation guarantee can be pinned in a test.
func applyProfileEdits(existing *appbsky.ActorProfile, displayName, description string) *appbsky.ActorProfile {
	if existing == nil {
		existing = &appbsky.ActorProfile{}
	}
	if displayName == "" {
		existing.DisplayName = nil
	} else {
		dn := displayName
		existing.DisplayName = &dn
	}
	if description == "" {
		existing.Description = nil
	} else {
		desc := description
		existing.Description = &desc
	}
	return existing
}

// isRecordNotFound reports whether err is an XRPC "record not found" error, as opposed
// to a network/auth failure. A first-time profile edit (the account never set a display
// name or avatar) has no self record yet; that case is safe to start from a fresh record.
// ANY OTHER error must abort the put — putting a fresh record over a transient fetch
// failure would clobber an existing avatar/banner (the data-loss risk of this flow).
func isRecordNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "recordnotfound") ||
		strings.Contains(msg, "could not locate record") ||
		strings.Contains(msg, "record not found")
}

// UpdateProfile edits the authenticated user's own profile record (display name and
// bio only) via a get-then-put against app.bsky.actor.profile/self. The existing
// record is fetched first and only DisplayName/Description are overwritten, so the
// avatar and banner blobs are preserved (see applyProfileEdits). Returns the updated
// profile as seen through getProfile.
func (c *Client) UpdateProfile(ctx context.Context, did, sessionID, displayName, description string) (*Profile, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	var existing *appbsky.ActorProfile
	// swapRecord is the compare-and-swap guard. indigo's RepoPutRecord_Input.SwapRecord
	// has NO omitempty, so a nil value serialises as "swapRecord": null — and in AT
	// Protocol a null swapRecord asserts "no record currently exists". For an account
	// that already has a profile record that assertion fails with InvalidSwap, deleting
	// nothing but rejecting the write. So we set swapRecord to the CURRENT record's CID:
	// a valid compare-and-swap that also prevents clobbering the avatar/banner if the
	// record changed between this read and the write. For a first-ever profile (no self
	// record yet) swapRecord stays nil → null → "assert none exists", the correct guard
	// for a create.
	var swapRecord *string
	got, err := comatproto.RepoGetRecord(ctx, apiClient, "", profileRecordCollection, did, profileRecordRkey)
	if err != nil {
		// A missing self record is fine (first-ever edit) — start fresh. Any other
		// error must abort: putting a fresh record would delete the avatar/banner.
		if !isRecordNotFound(err) {
			return nil, fmt.Errorf("getRecord profile: %w", err)
		}
	} else if got != nil {
		swapRecord = got.Cid
		if got.Value != nil {
			if p, ok := got.Value.Val.(*appbsky.ActorProfile); ok {
				existing = p
			}
		}
	}

	updated := applyProfileEdits(existing, displayName, description)
	if updated.CreatedAt == nil {
		now := time.Now().UTC().Format(time.RFC3339)
		updated.CreatedAt = &now
	}

	if _, err := comatproto.RepoPutRecord(ctx, apiClient, &comatproto.RepoPutRecord_Input{
		Collection: profileRecordCollection,
		Repo:       did,
		Rkey:       profileRecordRkey,
		Record:     &lexutil.LexiconTypeDecoder{Val: updated},
		SwapRecord: swapRecord,
	}); err != nil {
		return nil, fmt.Errorf("putRecord profile: %w", err)
	}

	// Return the fresh server view so the caller reflects exactly what persisted.
	return c.GetProfile(ctx, did, sessionID, did)
}
