package bluesky

import (
	"context"
	"errors"
	"testing"
)

// TestDetectMentions covers the mention regex boundary rules: valid domain-form
// handles (two-segment and subdomain), trailing-punctuation stripping, and the
// non-matches that keep it from firing on emails, mid-word '@', or a bare '@'.
func TestDetectMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string // expected handles (no '@'), in order
	}{
		{name: "two segment", text: "hi @rustypants.com", want: []string{"rustypants.com"}},
		{name: "bsky social", text: "@user.bsky.social hello", want: []string{"user.bsky.social"}},
		{name: "subdomain", text: "ping @a.b.c.example.com now", want: []string{"a.b.c.example.com"}},
		{name: "hyphen in segment", text: "@my-handle.bsky.social", want: []string{"my-handle.bsky.social"}},
		{name: "trailing period", text: "thanks @user.bsky.social.", want: []string{"user.bsky.social"}},
		{name: "trailing bang", text: "@user.bsky.social!", want: []string{"user.bsky.social"}},
		{name: "trailing comma then more", text: "@a.com, and @b.com", want: []string{"a.com", "b.com"}},
		{name: "start of string", text: "@a.com leads", want: []string{"a.com"}},

		{name: "email not matched", text: "mail me at a@b.com", want: nil},
		{name: "mid word at not matched", text: "foo@bar.com", want: nil},
		{name: "at alone not matched", text: "just @ here", want: nil},
		{name: "single segment not matched", text: "@foo bar", want: nil},
		{name: "at with no domain", text: "@ @@ @.", want: nil},
		{name: "hashtag not a mention", text: "#hello world", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectMentions(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("detectMentions(%q) = %d matches, want %d (%v)", tt.text, len(got), len(tt.want), got)
			}
			for i, m := range got {
				if m.handle != tt.want[i] {
					t.Errorf("match %d: handle = %q, want %q", i, m.handle, tt.want[i])
				}
				// Sanity: the reported byte range must slice back to "@handle".
				if got, want := tt.text[m.start:m.end], "@"+m.handle; got != want {
					t.Errorf("match %d: text[%d:%d] = %q, want %q", i, m.start, m.end, got, want)
				}
			}
		})
	}
}

// TestBuildQuoteEmbed verifies the app.bsky.embed.record embed is nil when no quote
// is supplied and carries the StrongRef when it is.
func TestBuildQuoteEmbed(t *testing.T) {
	if e := buildQuoteEmbed(nil); e != nil {
		t.Fatalf("buildQuoteEmbed(nil) = %+v, want nil", e)
	}

	const (
		uri = "at://did:plc:abc/app.bsky.feed.post/xyz"
		cid = "bafyquoted"
	)
	e := buildQuoteEmbed(&QuoteRef{URI: uri, CID: cid})
	if e == nil {
		t.Fatal("buildQuoteEmbed returned nil for a non-nil quote")
	}
	if e.EmbedRecord == nil {
		t.Fatal("embed is not an app.bsky.embed.record (EmbedRecord is nil)")
	}
	if e.EmbedRecord.Record == nil {
		t.Fatal("embed record StrongRef is nil")
	}
	if got := e.EmbedRecord.Record.Uri; got != uri {
		t.Errorf("embed record uri = %q, want %q", got, uri)
	}
	if got := e.EmbedRecord.Record.Cid; got != cid {
		t.Errorf("embed record cid = %q, want %q", got, cid)
	}
	// A quote embed must not accidentally populate the other embed variants.
	if e.EmbedImages != nil || e.EmbedVideo != nil || e.EmbedExternal != nil || e.EmbedRecordWithMedia != nil {
		t.Errorf("unexpected sibling embed variant set: %+v", e)
	}
}

// staticResolver resolves any handle present in the map; unknown handles error,
// simulating an NXDOMAIN / deleted-account / typo lookup failure.
func staticResolver(m map[string]string) handleResolver {
	return func(_ context.Context, handle string) (string, error) {
		if did, ok := m[handle]; ok {
			return did, nil
		}
		return "", errors.New("handle not found")
	}
}

func TestBuildMentionFacets_Offsets(t *testing.T) {
	resolve := staticResolver(map[string]string{
		"user.bsky.social": "did:plc:user",
	})

	facets := buildMentionFacets(context.Background(), "hi @user.bsky.social", resolve)
	if len(facets) != 1 {
		t.Fatalf("got %d facets, want 1", len(facets))
	}
	f := facets[0]
	if f.Index.ByteStart != 3 || f.Index.ByteEnd != 20 {
		t.Errorf("byte range = [%d,%d], want [3,20]", f.Index.ByteStart, f.Index.ByteEnd)
	}
	if f.Features[0].RichtextFacet_Mention == nil {
		t.Fatalf("feature is not a mention")
	}
	if did := f.Features[0].RichtextFacet_Mention.Did; did != "did:plc:user" {
		t.Errorf("did = %q, want did:plc:user", did)
	}
}

// TestBuildMentionFacets_EmojiOffset guards Gotcha 6: an emoji before the mention
// must not shift the UTF-8 byte offsets. "🎉" is 4 bytes, then a space, so the
// mention begins at byte 5.
func TestBuildMentionFacets_EmojiOffset(t *testing.T) {
	resolve := staticResolver(map[string]string{
		"user.bsky.social": "did:plc:user",
	})

	facets := buildMentionFacets(context.Background(), "🎉 @user.bsky.social", resolve)
	if len(facets) != 1 {
		t.Fatalf("got %d facets, want 1", len(facets))
	}
	if start, end := facets[0].Index.ByteStart, facets[0].Index.ByteEnd; start != 5 || end != 22 {
		t.Errorf("byte range = [%d,%d], want [5,22]", start, end)
	}
}

// TestBuildMentionFacets_UnresolvableStaysText verifies that a handle which fails
// to resolve produces no facet (left as plain text) while a resolvable one in the
// same post still gets its facet.
func TestBuildMentionFacets_UnresolvableStaysText(t *testing.T) {
	resolve := staticResolver(map[string]string{
		"real.bsky.social": "did:plc:real",
	})

	facets := buildMentionFacets(context.Background(),
		"@real.bsky.social and @nonexistent.fake.handle", resolve)
	if len(facets) != 1 {
		t.Fatalf("got %d facets, want 1 (only the resolvable handle)", len(facets))
	}
	if did := facets[0].Features[0].RichtextFacet_Mention.Did; did != "did:plc:real" {
		t.Errorf("did = %q, want did:plc:real", did)
	}
}

// TestBuildMentionFacets_DedupSingleLookup ensures the same handle appearing twice
// is resolved only once (per-request cache) but still yields a facet per occurrence.
func TestBuildMentionFacets_DedupSingleLookup(t *testing.T) {
	var calls int
	resolve := func(_ context.Context, handle string) (string, error) {
		calls++
		return "did:plc:dup", nil
	}

	facets := buildMentionFacets(context.Background(),
		"@dup.bsky.social vs @dup.bsky.social", resolve)
	if len(facets) != 2 {
		t.Fatalf("got %d facets, want 2", len(facets))
	}
	if calls != 1 {
		t.Errorf("resolver called %d times, want 1 (deduped)", calls)
	}
}

// TestBuildFacets_SuffixMentionAndOrdering exercises the combined body+suffix path:
// facet detection runs over the full text, so a mention living in a template suffix
// gets a facet alongside a hashtag, and the returned facets are byte-sorted.
func TestBuildFacets_SuffixMentionAndOrdering(t *testing.T) {
	resolve := staticResolver(map[string]string{
		"cohost.bsky.social": "did:plc:cohost",
	})

	// Simulates body "Great show tonight" + suffix "@cohost.bsky.social #showname".
	text := "Great show tonight @cohost.bsky.social #showname"
	facets := buildFacets(context.Background(), text, resolve)
	if len(facets) != 2 {
		t.Fatalf("got %d facets, want 2 (mention + hashtag)", len(facets))
	}

	// Byte-sorted ascending.
	if facets[0].Index.ByteStart >= facets[1].Index.ByteStart {
		t.Errorf("facets not byte-sorted: %d then %d",
			facets[0].Index.ByteStart, facets[1].Index.ByteStart)
	}

	// First is the mention, second is the hashtag.
	if facets[0].Features[0].RichtextFacet_Mention == nil {
		t.Errorf("first facet is not a mention")
	}
	if facets[1].Features[0].RichtextFacet_Tag == nil {
		t.Errorf("second facet is not a hashtag")
	}
	if tag := facets[1].Features[0].RichtextFacet_Tag; tag != nil && tag.Tag != "showname" {
		t.Errorf("hashtag tag = %q, want showname", tag.Tag)
	}
}
