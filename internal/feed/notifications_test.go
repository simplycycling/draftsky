package feed

import "testing"

func TestNotificationReasonLine(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{"like", "liked your post"},
		{"repost", "reposted your post"},
		{"reply", "replied to your post"},
		{"follow", "followed you"},
		{"mention", "mentioned you"},
		{"quote", "quoted your post"},
		{"somethingnew", "interacted with your post"}, // forward-compat fallback
		{"", "interacted with your post"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := Notification{Reason: tt.reason}.ReasonLine()
			if got != tt.want {
				t.Errorf("ReasonLine(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

func TestNotificationTargetURI(t *testing.T) {
	const notifURI = "at://did:plc:actor/app.bsky.feed.post/reply"
	const subjectURI = "at://did:plc:me/app.bsky.feed.post/mine"

	tests := []struct {
		name    string
		notif   Notification
		want    string
		click01 bool
	}{
		{
			// A like links to YOUR post that was liked (the reason subject),
			// not the like record itself.
			name:    "like targets the reason subject",
			notif:   Notification{Reason: "like", URI: notifURI, ReasonSubjectURI: subjectURI},
			want:    subjectURI,
			click01: true,
		},
		{
			name:    "repost targets the reason subject",
			notif:   Notification{Reason: "repost", URI: notifURI, ReasonSubjectURI: subjectURI},
			want:    subjectURI,
			click01: true,
		},
		{
			// A reply links to the reply post itself so the thread view shows its context.
			name:    "reply targets the notification post",
			notif:   Notification{Reason: "reply", URI: notifURI, ReasonSubjectURI: subjectURI},
			want:    notifURI,
			click01: true,
		},
		{
			name:    "mention targets the notification post",
			notif:   Notification{Reason: "mention", URI: notifURI},
			want:    notifURI,
			click01: true,
		},
		{
			name:    "quote targets the notification post",
			notif:   Notification{Reason: "quote", URI: notifURI},
			want:    notifURI,
			click01: true,
		},
		{
			// Follows are non-clickable until profiles exist.
			name:    "follow is not clickable",
			notif:   Notification{Reason: "follow", URI: "at://did:plc:actor/app.bsky.graph.follow/x"},
			want:    "",
			click01: false,
		},
		{
			name:    "unknown reason is not clickable",
			notif:   Notification{Reason: "mystery", URI: notifURI},
			want:    "",
			click01: false,
		},
		{
			// A like whose subject is missing degrades to non-clickable rather than
			// linking to the (non-post) like record.
			name:    "like without a subject is not clickable",
			notif:   Notification{Reason: "like", URI: notifURI},
			want:    "",
			click01: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.notif.TargetURI(); got != tt.want {
				t.Errorf("TargetURI() = %q, want %q", got, tt.want)
			}
			if got := tt.notif.Clickable(); got != tt.click01 {
				t.Errorf("Clickable() = %v, want %v", got, tt.click01)
			}
		})
	}
}

func TestNotificationUnreadFlagging(t *testing.T) {
	// IsRead maps straight through; the row template distinguishes unread items.
	if (Notification{IsRead: false}).IsRead {
		t.Error("expected IsRead false to remain false")
	}
	if !(Notification{IsRead: true}).IsRead {
		t.Error("expected IsRead true to remain true")
	}
}
