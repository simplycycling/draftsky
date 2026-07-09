package feed

import (
	"context"
	"errors"
	"fmt"
	"sort"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

var (
	ErrThreadNotFound = errors.New("thread: post not found")
	ErrThreadBlocked  = errors.New("thread: post blocked")
)

// ThreadView is the structured view of a post thread: ancestors in root-first
// order, the focal post, and its direct replies in ascending indexedAt order.
type ThreadView struct {
	Ancestors []PostView
	Focal     PostView
	Replies   []PostView
}

// GetThread fetches the thread rooted at or containing uri and returns a
// ThreadView. Returns ErrThreadNotFound or ErrThreadBlocked for those cases.
func (c *Client) GetThread(ctx context.Context, did, sessionID, uri string) (*ThreadView, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	// depth=1 — direct replies only; parentHeight=10 — up to 10 ancestor levels.
	out, err := appbsky.FeedGetPostThread(ctx, apiClient, 1, 10, uri)
	if err != nil {
		return nil, fmt.Errorf("getPostThread: %w", err)
	}

	if out.Thread == nil {
		return nil, ErrThreadNotFound
	}
	if out.Thread.FeedDefs_NotFoundPost != nil {
		return nil, ErrThreadNotFound
	}
	if out.Thread.FeedDefs_BlockedPost != nil {
		return nil, ErrThreadBlocked
	}
	focal := out.Thread.FeedDefs_ThreadViewPost
	if focal == nil {
		return nil, ErrThreadNotFound
	}

	// Walk parent chain innermost-first, collect ancestor PostViews.
	var ancestors []PostView
	for cur := focal.Parent; cur != nil; {
		if cur.FeedDefs_ThreadViewPost == nil {
			break // not-found or blocked ancestor — stop ascending
		}
		tvp := cur.FeedDefs_ThreadViewPost
		if tvp.Post != nil {
			ancestors = append(ancestors, postViewFromBsky(tvp.Post))
		}
		cur = tvp.Parent
	}
	// Reverse to root-first order.
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}

	focalPV := PostView{}
	if focal.Post != nil {
		focalPV = postViewFromBsky(focal.Post)
	}

	var replies []PostView
	for _, r := range focal.Replies {
		if r == nil || r.FeedDefs_ThreadViewPost == nil || r.FeedDefs_ThreadViewPost.Post == nil {
			continue
		}
		replies = append(replies, postViewFromBsky(r.FeedDefs_ThreadViewPost.Post))
	}
	sort.Slice(replies, func(i, j int) bool {
		return replies[i].IndexedAt < replies[j].IndexedAt
	})

	if ancestors == nil {
		ancestors = []PostView{}
	}
	if replies == nil {
		replies = []PostView{}
	}

	return &ThreadView{
		Ancestors: ancestors,
		Focal:     focalPV,
		Replies:   replies,
	}, nil
}
