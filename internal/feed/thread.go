package feed

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

var (
	ErrThreadNotFound = errors.New("thread: post not found")
	ErrThreadBlocked  = errors.New("thread: post blocked")
)

// ThreadEntry is one row in a thread's ancestor or reply list. Exactly one of Post or
// Unavailable is meaningful: when Unavailable is true the post is hidden (blocked,
// not-found, muted, or muted-word) and Post is nil — the template renders a labelled
// "unavailable" gap instead of the post, so a hole in the thread reads as intentional
// rather than as missing data. Reason is a short human label ("blocked", "muted", …).
type ThreadEntry struct {
	Post        *PostView
	Unavailable bool
	Reason      string
}

// ThreadView is the structured view of a post thread: ancestors in root-first
// order, the focal post, and its direct replies in ascending indexedAt order.
type ThreadView struct {
	Ancestors []ThreadEntry
	Focal     PostView
	Replies   []ThreadEntry
}

// threadEntryFor maps a single thread post to a ThreadEntry, honouring account-level
// moderation (muted/blocked author → an "unavailable" gap) and muted words (→ a "muted"
// gap). Returns a visible entry otherwise. now is the reference time for muted-word expiry.
func threadEntryFor(post *appbsky.FeedDefs_PostView, mutedWords []MutedWord, now time.Time) ThreadEntry {
	var authorViewer *appbsky.ActorDefs_ViewerState
	if post.Author != nil {
		authorViewer = post.Author.Viewer
	}
	if authorHidden(authorViewer) {
		return ThreadEntry{Unavailable: true, Reason: "muted or blocked"}
	}
	pv := postViewFromBsky(post)
	if postHiddenByMutedWords(mutedWords, pv, authorViewer, now) {
		return ThreadEntry{Unavailable: true, Reason: "muted"}
	}
	return ThreadEntry{Post: &pv}
}

// GetThread fetches the thread rooted at or containing uri and returns a
// ThreadView. Returns ErrThreadNotFound or ErrThreadBlocked for those cases.
//
// mutedWords filters ancestors and replies: a post from a muted/blocked account, or one
// matching a muted word, renders as an "unavailable" gap rather than vanishing — a
// truncated thread is confusing, a labelled gap is not. The focal post is always shown
// (the user navigated to it deliberately); a wholly blocked focal still returns
// ErrThreadBlocked, which the API surfaces server-side.
func (c *Client) GetThread(ctx context.Context, did, sessionID, uri string, mutedWords []MutedWord) (*ThreadView, error) {
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

	now := time.Now()

	// Walk parent chain innermost-first, collecting ancestor entries. A blocked or
	// not-found ancestor is a terminal node in the API response (it carries no parent
	// pointer), so it becomes one labelled gap and stops the ascent. A muted ancestor is
	// still a full ThreadViewPost, so it becomes a gap but the walk continues upward.
	var ancestors []ThreadEntry
	for cur := focal.Parent; cur != nil; {
		if cur.FeedDefs_ThreadViewPost == nil {
			reason := "unavailable"
			if cur.FeedDefs_BlockedPost != nil {
				reason = "blocked"
			}
			ancestors = append(ancestors, ThreadEntry{Unavailable: true, Reason: reason})
			break // not-found or blocked ancestor — cannot ascend past it
		}
		tvp := cur.FeedDefs_ThreadViewPost
		if tvp.Post != nil {
			ancestors = append(ancestors, threadEntryFor(tvp.Post, mutedWords, now))
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

	// Direct replies: blocked/not-found reply elements and muted/muted-word replies each
	// render as a labelled gap so the reply list doesn't silently lose rows.
	type sortableReply struct {
		entry     ThreadEntry
		indexedAt string
	}
	var sortable []sortableReply
	for _, r := range focal.Replies {
		if r == nil {
			continue
		}
		if r.FeedDefs_ThreadViewPost != nil && r.FeedDefs_ThreadViewPost.Post != nil {
			post := r.FeedDefs_ThreadViewPost.Post
			sortable = append(sortable, sortableReply{
				entry:     threadEntryFor(post, mutedWords, now),
				indexedAt: post.IndexedAt,
			})
			continue
		}
		reason := "unavailable"
		if r.FeedDefs_BlockedPost != nil {
			reason = "blocked"
		}
		// Blocked/not-found replies have no indexedAt; sort them to the end.
		sortable = append(sortable, sortableReply{
			entry:     ThreadEntry{Unavailable: true, Reason: reason},
			indexedAt: "",
		})
	}
	sort.SliceStable(sortable, func(i, j int) bool {
		// Entries without an indexedAt (blocked/not-found replies) sort to the end.
		if (sortable[i].indexedAt == "") != (sortable[j].indexedAt == "") {
			return sortable[i].indexedAt != ""
		}
		return sortable[i].indexedAt < sortable[j].indexedAt
	})
	replies := make([]ThreadEntry, 0, len(sortable))
	for _, s := range sortable {
		replies = append(replies, s.entry)
	}

	if ancestors == nil {
		ancestors = []ThreadEntry{}
	}

	return &ThreadView{
		Ancestors: ancestors,
		Focal:     focalPV,
		Replies:   replies,
	}, nil
}
