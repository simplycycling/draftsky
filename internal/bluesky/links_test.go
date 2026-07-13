package bluesky

import (
	"context"
	"testing"
)

// TestDetectLinks is the table of URL-detection behaviours DraftSky must match
// against Bluesky's own composer: explicit-scheme URLs, bare domains (with and
// without a path), trailing-punctuation stripping, position independence, and the
// bare-domain https:// prepend. Byte offsets are asserted so an emoji before a URL
// (Gotcha 6) cannot silently corrupt them.
func TestDetectLinks(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    []LinkMatch // Start/End are byte offsets of the LINK TEXT; URI resolved
		exclude []ByteRange
	}{
		{
			name: "scheme url",
			text: "see https://example.com/page now",
			want: []LinkMatch{{Start: 4, End: 28, URI: "https://example.com/page"}},
		},
		{
			name: "http scheme kept as-is",
			text: "http://example.com",
			want: []LinkMatch{{Start: 0, End: 18, URI: "http://example.com"}},
		},
		{
			name: "bare domain gets https prepended, text unchanged",
			text: "visit draftsky.social today",
			want: []LinkMatch{{Start: 6, End: 21, URI: "https://draftsky.social"}},
		},
		{
			name: "bare domain with path",
			text: "example.com/page here",
			want: []LinkMatch{{Start: 0, End: 16, URI: "https://example.com/page"}},
		},
		{
			name: "trailing period stripped (the classic case)",
			text: "see draftsky.social.",
			want: []LinkMatch{{Start: 4, End: 19, URI: "https://draftsky.social"}},
		},
		{
			name: "trailing comma stripped",
			text: "go to example.com, then leave",
			want: []LinkMatch{{Start: 6, End: 17, URI: "https://example.com"}},
		},
		{
			name: "url at start of string",
			text: "draftsky.social is live",
			want: []LinkMatch{{Start: 0, End: 15, URI: "https://draftsky.social"}},
		},
		{
			name: "url at end of string",
			text: "it is at draftsky.social",
			want: []LinkMatch{{Start: 9, End: 24, URI: "https://draftsky.social"}},
		},
		{
			name: "wrapped in parens keeps closing paren out",
			text: "(see example.com)",
			want: []LinkMatch{{Start: 5, End: 16, URI: "https://example.com"}},
		},
		{
			name: "invalid tld not linkified",
			text: "open photo.jpg please",
			want: nil,
		},
		{
			name: "abbreviation not linkified",
			text: "e.g. this is fine",
			want: nil,
		},
		{
			name: "two urls one space apart",
			text: "a.com b.com",
			want: []LinkMatch{
				{Start: 0, End: 5, URI: "https://a.com"},
				{Start: 6, End: 11, URI: "https://b.com"},
			},
		},
		{
			name: "emoji before url does not shift offsets",
			// "🎉" is 4 UTF-8 bytes, then a space, so the URL begins at byte 5.
			text: "🎉 draftsky.social",
			want: []LinkMatch{{Start: 5, End: 20, URI: "https://draftsky.social"}},
		},
		{
			name:    "excluded range (a mention) is not double-matched as a link",
			text:    "hi rogersherman.com bye",
			exclude: []ByteRange{{Start: 3, End: 19}}, // pretend a mention claimed it
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectLinks(tt.text, tt.exclude)
			if len(got) != len(tt.want) {
				t.Fatalf("DetectLinks(%q) = %d matches, want %d (%+v)", tt.text, len(got), len(tt.want), got)
			}
			for i, m := range got {
				w := tt.want[i]
				if m.Start != w.Start || m.End != w.End {
					t.Errorf("match %d: range [%d,%d], want [%d,%d]", i, m.Start, m.End, w.Start, w.End)
				}
				if m.URI != w.URI {
					t.Errorf("match %d: uri = %q, want %q", i, m.URI, w.URI)
				}
				// The reported range must slice back to the URL text (as typed).
				if slice := tt.text[m.Start:m.End]; len(slice) == 0 {
					t.Errorf("match %d: empty text slice for range [%d,%d]", i, m.Start, m.End)
				}
			}
		})
	}
}

// TestDetectLinks_MentionNotLinkified confirms the '@' boundary rule: a mention
// like "@rogersherman.com" is never a link even with NO exclusion passed, because
// the domain is preceded by '@' (not a URL boundary char). Belt-and-suspenders for
// the exclude-based guard in buildFacets.
func TestDetectLinks_MentionNotLinkified(t *testing.T) {
	if got := DetectLinks("ping @rogersherman.com now", nil); len(got) != 0 {
		t.Fatalf("mention @rogersherman.com was linkified: %+v", got)
	}
	// Same for a hashtag whose token contains a dot.
	if got := DetectLinks("go #draftsky.social go", nil); len(got) != 0 {
		t.Fatalf("hashtag #draftsky.social was linkified: %+v", got)
	}
}

// TestIsValidDomain spot-checks the TLD gate: real TLDs pass (incl. case-insensitive),
// non-delegated suffixes fail.
func TestIsValidDomain(t *testing.T) {
	tests := []struct {
		domain string
		want   bool
	}{
		{"draftsky.social", true},
		{"example.com", true},
		{"foo.co.uk", true},
		{"MixedCase.SOCIAL", true}, // lowercased before lookup
		{"photo.jpg", false},
		{"main.go", false},
		{"nodots", false},
	}
	for _, tt := range tests {
		if got := isValidDomain(tt.domain); got != tt.want {
			t.Errorf("isValidDomain(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
}

// TestBuildFacets_LinkWithTagAndMention exercises the full facet builder: a body
// with a URL, a hashtag, and a mention must yield one facet each of the right type,
// byte-sorted, with the URL carrying an app.bsky.richtext.facet#link. The mention's
// domain-form token must NOT also produce a link facet, and a "#tag.io"-style token
// stays a hashtag, never a link. Also covers a URL living in the suffix half.
func TestBuildFacets_LinkWithTagAndMention(t *testing.T) {
	resolve := staticResolver(map[string]string{
		"cohost.bsky.social": "did:plc:cohost",
	})

	// body: "New post at draftsky.social by @cohost.bsky.social"
	// suffix-ish: "#launch"
	text := "New post at draftsky.social by @cohost.bsky.social #launch"
	facets := buildFacets(context.Background(), text, resolve)

	var links, mentions, tags int
	var lastStart int64 = -1
	for _, f := range facets {
		if f.Index.ByteStart < lastStart {
			t.Errorf("facets not byte-sorted at start=%d (prev %d)", f.Index.ByteStart, lastStart)
		}
		lastStart = f.Index.ByteStart
		feat := f.Features[0]
		switch {
		case feat.RichtextFacet_Link != nil:
			links++
			if uri := feat.RichtextFacet_Link.Uri; uri != "https://draftsky.social" {
				t.Errorf("link uri = %q, want https://draftsky.social", uri)
			}
			// The facet range must cover the bare text "draftsky.social" as typed.
			if slice := text[f.Index.ByteStart:f.Index.ByteEnd]; slice != "draftsky.social" {
				t.Errorf("link text range = %q, want draftsky.social", slice)
			}
		case feat.RichtextFacet_Mention != nil:
			mentions++
		case feat.RichtextFacet_Tag != nil:
			tags++
		}
	}
	if links != 1 || mentions != 1 || tags != 1 {
		t.Fatalf("facet counts: links=%d mentions=%d tags=%d, want 1/1/1 (all: %d)",
			links, mentions, tags, len(facets))
	}
}
