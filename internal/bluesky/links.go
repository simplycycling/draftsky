package bluesky

import (
	"regexp"
	"strings"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
)

// urlRe mirrors the URL_REGEX in Bluesky's own composer (@atproto/api rich-text
// detection). It matches either an explicit-scheme URL (http/https) or a bare
// domain with an optional path. The leading (?:^|[\s(]) anchors the token to a
// boundary — start of text, whitespace, or an opening paren — so a URL is never
// detected mid-word. That boundary is what keeps @-mentions and #-hashtags from
// ever forming a link: the domain inside "@rogersherman.com" / "#draftsky.social"
// is preceded by '@'/'#', which is not a boundary character, so it does not match.
// The (?i) flag lets uppercase match; the TLD check lowercases the candidate.
//
//	group 1: the whole URL token (this is the facet's byte range)
//	group 2: the scheme-URL form (non-empty only for a scheme URL)
//	group 3: the bare-domain form (non-empty only for a bare domain)
//	group 4: the domain portion of the bare form (used for TLD validation)
var urlRe = regexp.MustCompile(`(?i)(?:^|[\s(])((https?://\S+)|(([a-z][a-z0-9]*(?:\.[a-z0-9]+)+)\S*))`)

// ByteRange is a half-open [Start, End) range of UTF-8 byte offsets into a string.
type ByteRange struct {
	Start, End int
}

// LinkMatch is a detected URL: the byte range of the link TEXT in the source
// string and the resolved URI. For a scheme URL the URI equals the text (minus
// stripped trailing punctuation); for a bare domain the URI has "https://"
// prepended while the text stays exactly as typed.
type LinkMatch struct {
	Start, End int
	URI        string
}

// urlTrailingPunct is the single-character trailing punctuation set Bluesky strips
// from the end of a detected URL (its detectFacets strips one such char, then one
// unmatched ')'). These are all ASCII, so a byte-wise check is UTF-8 safe.
const urlTrailingPunct = ".,;:!?"

// DetectLinks finds URL tokens in text and returns their byte ranges and resolved
// URIs, byte-offset ordered as the regex yields them (already ascending). A match
// whose range overlaps any range in exclude is dropped — callers pass the byte
// ranges of already-claimed facets (hashtags, mentions) so a token can never be
// two facets at once. Byte offsets come straight from the regex engine (UTF-8 byte
// indices — Gotcha 6), so emoji earlier in the text never corrupt them.
//
// Bare domains are validated against the IANA TLD set (isValidDomain) exactly as
// Bluesky does, so "photo.jpg" or "e.g" are not linkified while "draftsky.social"
// is. Trailing punctuation is stripped to match Bluesky: one char from the
// urlTrailingPunct set, then one ')' when the token has no '(' — so
// "(see example.com)" links "example.com" and "draftsky.social." links without the
// period.
func DetectLinks(text string, exclude []ByteRange) []LinkMatch {
	idxs := urlRe.FindAllStringSubmatchIndex(text, -1)

	var out []LinkMatch
	for _, m := range idxs {
		// group 1 (indices m[2]/m[3]) is the whole URL token.
		start, end := m[2], m[3]
		token := text[start:end]

		// group 2 (m[4]/m[5]) is set only for the explicit-scheme form.
		isScheme := m[4] != -1
		if !isScheme {
			// group 4 (m[8]/m[9]) is the domain; reject if its TLD is unknown.
			domain := text[m[8]:m[9]]
			if !isValidDomain(domain) {
				continue
			}
		}

		// Strip trailing punctuation, then one unmatched ')', mirroring Bluesky.
		// Trailing chars are ASCII, so slicing on the last byte is UTF-8 safe.
		if n := len(token); n > 0 && strings.IndexByte(urlTrailingPunct, token[n-1]) >= 0 {
			token = token[:n-1]
		}
		if n := len(token); n > 0 && token[n-1] == ')' && !strings.Contains(token, "(") {
			token = token[:n-1]
		}
		if token == "" {
			continue
		}
		end = start + len(token)

		if overlapsAny(start, end, exclude) {
			continue
		}

		uri := token
		if !isScheme {
			uri = "https://" + token
		}
		out = append(out, LinkMatch{Start: start, End: end, URI: uri})
	}
	return out
}

// isValidDomain reports whether domain's final label is a delegated TLD, matching
// Bluesky's isValidDomain. The regex guarantees at least one '.', so the last label
// is well-defined; the candidate is lowercased before lookup (domains are
// case-insensitive, and tldSet is lowercase).
func isValidDomain(domain string) bool {
	i := strings.LastIndexByte(domain, '.')
	if i < 0 {
		return false
	}
	_, ok := tldSet[strings.ToLower(domain[i+1:])]
	return ok
}

// overlapsAny reports whether [start, end) overlaps any range in ranges.
func overlapsAny(start, end int, ranges []ByteRange) bool {
	for _, r := range ranges {
		if start < r.End && r.Start < end {
			return true
		}
	}
	return false
}

// buildLinkFacets returns app.bsky.richtext.facet#link facets for the URLs in
// text, skipping any that overlap the excluded (hashtag/mention) ranges.
func buildLinkFacets(text string, exclude []ByteRange) []*appbsky.RichtextFacet {
	matches := DetectLinks(text, exclude)
	var facets []*appbsky.RichtextFacet
	for _, m := range matches {
		facets = append(facets, &appbsky.RichtextFacet{
			Index: &appbsky.RichtextFacet_ByteSlice{
				ByteStart: int64(m.Start),
				ByteEnd:   int64(m.End),
			},
			Features: []*appbsky.RichtextFacet_Features_Elem{
				{
					RichtextFacet_Link: &appbsky.RichtextFacet_Link{
						Uri: m.URI,
					},
				},
			},
		})
	}
	return facets
}
