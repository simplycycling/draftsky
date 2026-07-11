package handlers

import (
	"testing"
)

// TestValidatePostRefs covers the reply/quote/text combination rules that
// HandleCreatePost enforces before submitting to Bluesky.
func TestValidatePostRefs(t *testing.T) {
	const (
		pURI = "at://did:plc:x/app.bsky.feed.post/parent"
		pCID = "bafyparent"
		rURI = "at://did:plc:x/app.bsky.feed.post/root"
		rCID = "bafyroot"
		qURI = "at://did:plc:y/app.bsky.feed.post/quoted"
		qCID = "bafyquoted"
	)

	tests := []struct {
		name      string
		req       createPostRequest
		wantErr   bool
		wantReply bool
		wantQuote bool
	}{
		{
			name: "plain post with text",
			req:  createPostRequest{Text: "hello"},
		},
		{
			name:    "empty text without quote is rejected",
			req:     createPostRequest{Text: ""},
			wantErr: true,
		},
		{
			name:    "whitespace-only text without quote is rejected",
			req:     createPostRequest{Text: "   \n  "},
			wantErr: true,
		},
		{
			name:      "quote with comment",
			req:       createPostRequest{Text: "great post", QuoteURI: qURI, QuoteCID: qCID},
			wantQuote: true,
		},
		{
			name:      "bare quote with empty text is allowed",
			req:       createPostRequest{Text: "", QuoteURI: qURI, QuoteCID: qCID},
			wantQuote: true,
		},
		{
			name:    "quote uri without cid is rejected",
			req:     createPostRequest{Text: "hi", QuoteURI: qURI},
			wantErr: true,
		},
		{
			name:    "quote cid without uri is rejected",
			req:     createPostRequest{Text: "hi", QuoteCID: qCID},
			wantErr: true,
		},
		{
			name: "full reply",
			req: createPostRequest{
				Text: "my reply", ReplyParentURI: pURI, ReplyParentCID: pCID,
				ReplyRootURI: rURI, ReplyRootCID: rCID,
			},
			wantReply: true,
		},
		{
			name:    "partial reply is rejected",
			req:     createPostRequest{Text: "hi", ReplyParentURI: pURI},
			wantErr: true,
		},
		{
			name: "quote and reply together is rejected",
			req: createPostRequest{
				Text: "no", ReplyParentURI: pURI, ReplyParentCID: pCID,
				ReplyRootURI: rURI, ReplyRootCID: rCID,
				QuoteURI: qURI, QuoteCID: qCID,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs, verr := validatePostRefs(tt.req)
			if tt.wantErr {
				if verr == "" {
					t.Fatalf("expected a validation error, got none (refs=%+v)", refs)
				}
				return
			}
			if verr != "" {
				t.Fatalf("unexpected validation error: %q", verr)
			}
			if tt.wantReply && refs.reply == nil {
				t.Errorf("expected reply refs, got nil")
			}
			if !tt.wantReply && refs.reply != nil {
				t.Errorf("unexpected reply refs: %+v", refs.reply)
			}
			if tt.wantQuote {
				if refs.quote == nil {
					t.Fatalf("expected quote refs, got nil")
				}
				if refs.quote.URI != qURI || refs.quote.CID != qCID {
					t.Errorf("quote refs = %+v, want {URI:%q CID:%q}", refs.quote, qURI, qCID)
				}
			}
			if !tt.wantQuote && refs.quote != nil {
				t.Errorf("unexpected quote refs: %+v", refs.quote)
			}
		})
	}
}
