package feed

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/atclient"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// PostAuthor is the clean author representation returned in feed responses.
type PostAuthor struct {
	DID         string  `json:"did"`
	Handle      string  `json:"handle"`
	DisplayName string  `json:"display_name,omitempty"`
	Avatar      *string `json:"avatar,omitempty"`
}

// PostImage is a single image attached to a post.
type PostImage struct {
	Thumb    string `json:"thumb"`
	Fullsize string `json:"fullsize"`
	Alt      string `json:"alt"`
}

// PostExternalLink is an external URL card attached to a post (app.bsky.embed.external#view).
type PostExternalLink struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumb       string `json:"thumb,omitempty"`
}

// PostView is the clean JSON representation of a single post in a feed.
// No indigo types leak out of this struct.
type PostView struct {
	URI         string      `json:"uri"`
	CID         string      `json:"cid"`
	Author      PostAuthor  `json:"author"`
	Text        string      `json:"text"`
	IndexedAt   string      `json:"indexed_at"`
	LikeCount   int64       `json:"like_count"`
	RepostCount int64       `json:"repost_count"`
	ReplyCount  int64       `json:"reply_count"`
	LikedByMe    bool              `json:"liked_by_me"`
	LikeURI      string            `json:"like_uri,omitempty"`
	RepostedByMe bool              `json:"reposted_by_me"`
	RepostURI    string            `json:"repost_uri,omitempty"`
	Images       []PostImage       `json:"images,omitempty"`
	ExternalLink *PostExternalLink `json:"external_link,omitempty"`
	// ReplyRootURI / ReplyRootCID are populated from the post's own reply.root when
	// the post is itself a reply. Empty for top-level posts.
	ReplyRootURI string `json:"reply_root_uri,omitempty"`
	ReplyRootCID string `json:"reply_root_cid,omitempty"`
	// RepostedBy is non-nil when the post appears in the timeline because someone the
	// user follows reposted it. Nil for original posts and all hashtag-feed results.
	RepostedBy *PostAuthor `json:"reposted_by,omitempty"`
}

// FeedPage is a page of posts with an optional cursor for the next page.
// NextCursor is an empty string when no further pages are available.
type FeedPage struct {
	Posts      []PostView `json:"posts"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// Client wraps the OAuth ClientApp for feed fetching.
type Client struct {
	app *oauth.ClientApp
}

// New returns a Client backed by the given ClientApp.
func New(app *oauth.ClientApp) *Client {
	return &Client{app: app}
}

// resumeAPIClient resolves the OAuth session for did/sessionID and returns an
// authenticated API client ready to call Bluesky XRPC endpoints.
func (c *Client) resumeAPIClient(ctx context.Context, did, sessionID string) (*atclient.APIClient, error) {
	d, err := syntax.ParseDID(did)
	if err != nil {
		return nil, fmt.Errorf("invalid DID %q: %w", did, err)
	}
	sess, err := c.app.ResumeSession(ctx, d, sessionID)
	if err != nil {
		return nil, fmt.Errorf("resume session: %w", err)
	}
	return sess.APIClient(), nil
}

// GetFollowingFeed returns a page of the authenticated user's Following feed,
// using app.bsky.feed.getTimeline. The cursor is the opaque string returned
// in the previous response; pass an empty string for the first page.
func (c *Client) GetFollowingFeed(ctx context.Context, did, sessionID, cursor string, limit int) (*FeedPage, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	out, err := appbsky.FeedGetTimeline(ctx, apiClient, "", cursor, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("getTimeline: %w", err)
	}

	posts := make([]PostView, 0, len(out.Feed))
	for _, item := range out.Feed {
		if item == nil || item.Post == nil {
			continue
		}
		pv := postViewFromBsky(item.Post)
		if item.Reason != nil && item.Reason.FeedDefs_ReasonRepost != nil {
			r := item.Reason.FeedDefs_ReasonRepost
			if r.By != nil {
				by := PostAuthor{
					DID:    r.By.Did,
					Handle: r.By.Handle,
					Avatar: r.By.Avatar,
				}
				if r.By.DisplayName != nil {
					by.DisplayName = *r.By.DisplayName
				}
				pv.RepostedBy = &by
			}
		}
		posts = append(posts, pv)
	}

	var nextCursor string
	if out.Cursor != nil {
		nextCursor = *out.Cursor
	}

	return &FeedPage{Posts: posts, NextCursor: nextCursor}, nil
}

// GetHashtagFeed fetches posts for each tag concurrently via app.bsky.feed.searchPosts,
// merges the results, deduplicates by URI, sorts by indexedAt descending, and applies
// cursor-based pagination. The cursor is an indexedAt timestamp — only posts with
// indexedAt strictly before the cursor are returned. Tags may or may not include a
// leading '#'; it is stripped and re-added for the query.
func (c *Client) GetHashtagFeed(ctx context.Context, did, sessionID string, tags []string, cursor string, limit int) (*FeedPage, error) {
	if len(tags) == 0 {
		return &FeedPage{Posts: []PostView{}}, nil
	}

	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	type searchResult struct {
		posts []*appbsky.FeedDefs_PostView
		err   error
	}

	results := make(chan searchResult, len(tags))
	var wg sync.WaitGroup

	for _, tag := range tags {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			tagName := strings.TrimPrefix(t, "#")
			if tagName == "" {
				results <- searchResult{}
				return
			}
			// Use q="#tag" so the query is human-readable, and tag=["tagName"] for
			// exact facet matching. Fetch up to 100 results per tag to give the
			// merge a large pool before applying the caller's limit.
			out, err := appbsky.FeedSearchPosts(
				ctx, apiClient,
				"",            // author
				"",            // cursor (we do our own cursor logic after merging)
				"",            // domain
				"",            // lang
				100,           // limit per-tag — maximise merge pool
				"",            // mentions
				"#"+tagName,   // q
				"",            // since
				"latest",      // sort
				[]string{tagName}, // tag (facet filter)
				"",            // until
				"",            // url
			)
			if err != nil {
				results <- searchResult{err: fmt.Errorf("searchPosts(%q): %w", tagName, err)}
				return
			}
			results <- searchResult{posts: out.Posts}
		}(tag)
	}

	wg.Wait()
	close(results)

	// Merge and deduplicate by URI.
	seen := make(map[string]struct{})
	var all []PostView
	for r := range results {
		if r.err != nil {
			return nil, r.err
		}
		for _, p := range r.posts {
			if p == nil {
				continue
			}
			if _, dup := seen[p.Uri]; dup {
				continue
			}
			seen[p.Uri] = struct{}{}
			all = append(all, postViewFromBsky(p))
		}
	}

	// Sort by indexedAt descending (lexicographic on RFC3339 strings is correct).
	sort.Slice(all, func(i, j int) bool {
		return all[i].IndexedAt > all[j].IndexedAt
	})

	// Apply cursor: return only posts strictly before the cursor timestamp.
	if cursor != "" {
		n := 0
		for _, p := range all {
			if p.IndexedAt < cursor {
				all[n] = p
				n++
			}
		}
		all = all[:n]
	}

	// Paginate: if more results than the requested limit exist, set a next cursor.
	var nextCursor string
	if len(all) > limit {
		// NextCursor is the indexedAt of the last post we're returning; the next
		// page will filter for posts strictly before that timestamp.
		nextCursor = all[limit-1].IndexedAt
		all = all[:limit]
	}

	// Guarantee a non-nil slice so JSON encodes as [] not null.
	if all == nil {
		all = []PostView{}
	}

	return &FeedPage{Posts: all, NextCursor: nextCursor}, nil
}

// postViewFromBsky converts an indigo FeedDefs_PostView to a clean PostView,
// extracting the post text from the record's decoded value.
func postViewFromBsky(pv *appbsky.FeedDefs_PostView) PostView {
	v := PostView{
		URI:       pv.Uri,
		CID:       pv.Cid,
		IndexedAt: pv.IndexedAt,
	}

	if pv.Author != nil {
		v.Author = PostAuthor{
			DID:    pv.Author.Did,
			Handle: pv.Author.Handle,
			Avatar: pv.Author.Avatar,
		}
		if pv.Author.DisplayName != nil {
			v.Author.DisplayName = *pv.Author.DisplayName
		}
	}

	if pv.LikeCount != nil {
		v.LikeCount = *pv.LikeCount
	}
	if pv.RepostCount != nil {
		v.RepostCount = *pv.RepostCount
	}
	if pv.ReplyCount != nil {
		v.ReplyCount = *pv.ReplyCount
	}

	if pv.Record != nil {
		if fp, ok := pv.Record.Val.(*appbsky.FeedPost); ok {
			v.Text = fp.Text
			if fp.Reply != nil && fp.Reply.Root != nil {
				v.ReplyRootURI = fp.Reply.Root.Uri
				v.ReplyRootCID = fp.Reply.Root.Cid
			}
		}
	}

	if pv.Viewer != nil && pv.Viewer.Like != nil {
		v.LikedByMe = true
		v.LikeURI = *pv.Viewer.Like
	}
	if pv.Viewer != nil && pv.Viewer.Repost != nil {
		v.RepostedByMe = true
		v.RepostURI = *pv.Viewer.Repost
	}

	if pv.Embed != nil {
		if pv.Embed.EmbedImages_View != nil {
			for _, img := range pv.Embed.EmbedImages_View.Images {
				if img != nil {
					v.Images = append(v.Images, PostImage{
						Thumb:    img.Thumb,
						Fullsize: img.Fullsize,
						Alt:      img.Alt,
					})
				}
			}
		}
		if ext := pv.Embed.EmbedExternal_View; ext != nil && ext.External != nil {
			el := &PostExternalLink{
				URI:         ext.External.Uri,
				Title:       ext.External.Title,
				Description: ext.External.Description,
			}
			if ext.External.Thumb != nil {
				el.Thumb = *ext.External.Thumb
			}
			v.ExternalLink = el
		}
	}

	return v
}
