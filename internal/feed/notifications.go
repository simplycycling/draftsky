package feed

import (
	"context"
	"fmt"
	"time"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

// Notification is the clean representation of a single Bluesky notification.
// No indigo types leak out of this struct.
type Notification struct {
	// URI is the notification record's own AT URI. For a reply/mention/quote this
	// is the actor's post; for a like/repost it is the like/repost record; for a
	// follow it is the follow record.
	URI    string     `json:"uri"`
	CID    string     `json:"cid"`
	Author PostAuthor `json:"author"`
	// Reason is the raw notification reason: like | repost | follow | mention | reply | quote.
	Reason string `json:"reason"`
	// ReasonSubjectURI is the AT URI of the post the reason refers to — for a like or
	// repost this is *your* post that was acted on. Empty for follows.
	ReasonSubjectURI string `json:"reason_subject_uri,omitempty"`
	IsRead           bool   `json:"is_read"`
	IndexedAt        string `json:"indexed_at"`
	// Snippet is the post text carried by the notification record itself, available
	// without an extra call only for reasons whose record is a post (reply, mention,
	// quote). Empty for like/repost/follow (their records are not posts).
	Snippet string `json:"snippet,omitempty"`
}

// ReasonLine returns the human-readable sentence describing why this notification
// was delivered, e.g. "liked your post".
func (n Notification) ReasonLine() string {
	switch n.Reason {
	case "like":
		return "liked your post"
	case "repost":
		return "reposted your post"
	case "reply":
		return "replied to your post"
	case "follow":
		return "followed you"
	case "mention":
		return "mentioned you"
	case "quote":
		return "quoted your post"
	default:
		return "interacted with your post"
	}
}

// TargetURI returns the AT URI a notification row should link through to, or ""
// when the notification is not clickable. For like/repost the best destination is
// your own post that was acted on (ReasonSubjectURI); for reply/mention/quote it is
// the actor's post itself (URI), whose thread view shows the surrounding context.
// Follows (and unknown reasons) are non-clickable for now — profiles don't exist yet.
func (n Notification) TargetURI() string {
	switch n.Reason {
	case "like", "repost":
		return n.ReasonSubjectURI
	case "reply", "mention", "quote":
		return n.URI
	default: // follow, unknown
		return ""
	}
}

// Clickable reports whether the row should be rendered as a link to a thread.
func (n Notification) Clickable() bool {
	return n.TargetURI() != ""
}

// NotificationPage is a page of notifications with an optional cursor for the next
// page. NextCursor is an empty string when no further pages are available.
type NotificationPage struct {
	Notifications []Notification `json:"notifications"`
	NextCursor    string         `json:"next_cursor,omitempty"`
}

// mapNotification converts an indigo notification to a clean Notification, extracting
// the post-text snippet from the record only when the record is itself a post.
func mapNotification(n *appbsky.NotificationListNotifications_Notification) Notification {
	out := Notification{
		URI:       n.Uri,
		CID:       n.Cid,
		Reason:    n.Reason,
		IsRead:    n.IsRead,
		IndexedAt: n.IndexedAt,
	}
	if n.ReasonSubject != nil {
		out.ReasonSubjectURI = *n.ReasonSubject
	}
	if n.Author != nil {
		out.Author = PostAuthor{
			DID:    n.Author.Did,
			Handle: n.Author.Handle,
			Avatar: n.Author.Avatar,
		}
		if n.Author.DisplayName != nil {
			out.Author.DisplayName = *n.Author.DisplayName
		}
	}
	// The record is a post only for reply/mention/quote; like/repost/follow records
	// carry no text. Pull the snippet from the decoded record without an extra call.
	if n.Record != nil {
		if fp, ok := n.Record.Val.(*appbsky.FeedPost); ok {
			out.Snippet = fp.Text
		}
	}
	return out
}

// GetNotifications returns a page of the authenticated user's notifications via
// app.bsky.notification.listNotifications. cursor is the opaque pagination cursor
// from the previous response; pass "" for the first page. All reasons are included.
func (c *Client) GetNotifications(ctx context.Context, did, sessionID, cursor string, limit int) (*NotificationPage, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return nil, err
	}

	out, err := appbsky.NotificationListNotifications(ctx, apiClient, cursor, int64(limit), false, nil, "")
	if err != nil {
		return nil, fmt.Errorf("listNotifications: %w", err)
	}

	items := make([]Notification, 0, len(out.Notifications))
	for _, n := range out.Notifications {
		if n == nil {
			continue
		}
		items = append(items, mapNotification(n))
	}

	var nextCursor string
	if out.Cursor != nil {
		nextCursor = *out.Cursor
	}
	return &NotificationPage{Notifications: items, NextCursor: nextCursor}, nil
}

// GetUnreadCount returns the number of unread notifications via
// app.bsky.notification.getUnreadCount.
func (c *Client) GetUnreadCount(ctx context.Context, did, sessionID string) (int64, error) {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return 0, err
	}

	out, err := appbsky.NotificationGetUnreadCount(ctx, apiClient, false, "")
	if err != nil {
		return 0, fmt.Errorf("getUnreadCount: %w", err)
	}
	return out.Count, nil
}

// UpdateSeen marks all notifications as seen up to now via
// app.bsky.notification.updateSeen, which clears the unread count.
func (c *Client) UpdateSeen(ctx context.Context, did, sessionID string) error {
	apiClient, err := c.resumeAPIClient(ctx, did, sessionID)
	if err != nil {
		return err
	}

	seenAt := time.Now().UTC().Format(time.RFC3339)
	if err := appbsky.NotificationUpdateSeen(ctx, apiClient, &appbsky.NotificationUpdateSeen_Input{SeenAt: seenAt}); err != nil {
		return fmt.Errorf("updateSeen: %w", err)
	}
	return nil
}
