package bluesky

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	lexutil "github.com/bluesky-social/indigo/lex/util"
)

// IsRateLimitError returns true if err is an HTTP 429 response from the Bluesky PDS.
// Indigo surfaces upstream HTTP errors with the status code in the message.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") || strings.Contains(msg, "ratelimitexceeded")
}

// hashtagRe matches hashtags — a `#` not preceded by a non-whitespace character,
// followed by a non-digit, non-whitespace character, then any non-whitespace characters.
// It is anchored to either the start of string or a whitespace boundary so that
// URLs like https://example.com/#anchor are not treated as hashtags.
var hashtagRe = regexp.MustCompile(`(?:^|[\s])(#[^\d\s]\S*)`)

// mentionRe matches @-mentions of domain-form handles (e.g. @rustypants.com,
// @user.bsky.social). Like hashtagRe it is anchored to the start of string or a
// whitespace boundary so the '@' must begin the token — this excludes email
// addresses (a@b.com) and mid-word '@'. A handle is two or more dot-joined
// segments of [a-zA-Z0-9-], so a bare '@foo' (no domain) is not matched. Group 1
// is the whole "@handle" token; trailing punctuation is stripped afterwards just
// as hashtags are.
var mentionRe = regexp.MustCompile(`(?:^|[\s])(@[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+)`)

// mentionResolveTimeout bounds each individual com.atproto.identity.resolveHandle
// lookup so a slow or unreachable PDS cannot stall a post. A handle that does not
// resolve within this window is treated as unresolved and left as plain text.
const mentionResolveTimeout = 3 * time.Second

// handleResolver resolves a bare handle (no leading '@') to a DID. It is injected
// into buildFacets so mention-facet construction can be unit-tested without a live
// PDS. The production implementation wraps com.atproto.identity.resolveHandle.
type handleResolver func(ctx context.Context, handle string) (string, error)

// Poster wraps the OAuth client and handles posting to Bluesky.
type Poster struct {
	app *oauth.ClientApp
}

// New returns a Poster backed by the given ClientApp.
func New(app *oauth.ClientApp) *Poster {
	return &Poster{app: app}
}

// PostResult holds the outcome of a successful post.
type PostResult struct {
	URI string
	CID string
}

// ReplyRefs carries the AT Protocol strong-ref pairs required to thread a reply.
// For a reply to a top-level post, Root and Parent point to the same post.
// For a reply to a reply, Parent is the direct parent and Root is the thread root.
type ReplyRefs struct {
	ParentURI string
	ParentCID string
	RootURI   string
	RootCID   string
}

// QuoteRef carries the StrongRef of the post being quoted. When present it is
// attached to the new post as an app.bsky.embed.record embed, producing a Bluesky
// quote post. Quote and reply are mutually exclusive in v1 (enforced at the handler).
type QuoteRef struct {
	URI string
	CID string
}

// buildQuoteEmbed returns the app.bsky.embed.record embed for a quoted post, or nil
// when quote is nil. Extracted so the embed construction can be unit-tested without a
// live session.
func buildQuoteEmbed(quote *QuoteRef) *appbsky.FeedPost_Embed {
	if quote == nil {
		return nil
	}
	return &appbsky.FeedPost_Embed{
		EmbedRecord: &appbsky.EmbedRecord{
			Record: &comatproto.RepoStrongRef{
				Uri: quote.URI,
				Cid: quote.CID,
			},
		},
	}
}

// Post submits a post to Bluesky on behalf of the user identified by did/sessionID.
// If suffix is non-empty it is appended to text with a space separator.
// Hashtags in the final text are detected and annotated as richtext facets.
// If reply is non-nil the post is submitted as a reply with the given thread refs.
// If quote is non-nil an app.bsky.embed.record embed is attached, making the post a
// quote post. A bare quote (empty text) is valid. Facets and the embed coexist.
func (p *Poster) Post(ctx context.Context, didStr, sessionID, text, suffix string, reply *ReplyRefs, quote *QuoteRef) (*PostResult, error) {
	did, err := syntax.ParseDID(didStr)
	if err != nil {
		return nil, fmt.Errorf("invalid DID %q: %w", didStr, err)
	}

	sess, err := p.app.ResumeSession(ctx, did, sessionID)
	if err != nil {
		return nil, fmt.Errorf("resume session: %w", err)
	}

	fullText := text
	if suffix != "" {
		normalised := strings.ReplaceAll(text, "\r\n", "\n")
		trimmed := strings.TrimRight(normalised, " ")
		if strings.HasSuffix(trimmed, "\n") {
			fullText = trimmed + suffix
		} else {
			fullText = trimmed + " " + suffix
		}
	}
	fullText = strings.TrimSpace(fullText)

	apiClient := sess.APIClient()

	// Facet detection runs on the combined body+suffix text, so a mention that
	// lives in a template suffix (e.g. a collab template) is annotated too.
	resolve := func(ctx context.Context, handle string) (string, error) {
		out, err := comatproto.IdentityResolveHandle(ctx, apiClient, handle)
		if err != nil {
			return "", err
		}
		return out.Did, nil
	}
	facets := buildFacets(ctx, fullText, resolve)

	post := &appbsky.FeedPost{
		Text:      fullText,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Facets:    facets,
	}

	if reply != nil {
		post.Reply = &appbsky.FeedPost_ReplyRef{
			Root: &comatproto.RepoStrongRef{
				Uri: reply.RootURI,
				Cid: reply.RootCID,
			},
			Parent: &comatproto.RepoStrongRef{
				Uri: reply.ParentURI,
				Cid: reply.ParentCID,
			},
		}
	}

	if embed := buildQuoteEmbed(quote); embed != nil {
		post.Embed = embed
	}

	out, err := comatproto.RepoCreateRecord(ctx, apiClient, &comatproto.RepoCreateRecord_Input{
		Collection: "app.bsky.feed.post",
		Repo:       didStr,
		Record:     &lexutil.LexiconTypeDecoder{Val: post},
	})
	if err != nil {
		return nil, fmt.Errorf("create record: %w", err)
	}

	return &PostResult{URI: out.Uri, CID: out.Cid}, nil
}

// ExtractHashtags returns the unique hashtag strings (without the leading '#') found in text.
func ExtractHashtags(text string) []string {
	matches := hashtagRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]struct{}, len(matches))
	var tags []string
	for _, m := range matches {
		tag := strings.TrimRight(m[1], ".,!?;:'\")")
		tag = strings.TrimPrefix(tag, "#")
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

// buildHashtagFacets scans text for hashtags and returns the corresponding facets.
// Byte offsets are computed over the UTF-8 encoded text, as required by the AT Protocol.
func buildHashtagFacets(text string) []*appbsky.RichtextFacet {
	textBytes := []byte(text)
	matches := hashtagRe.FindAllStringSubmatchIndex(text, -1)

	var facets []*appbsky.RichtextFacet
	for _, m := range matches {
		// m[2]/m[3] are the byte indices of the first (and only) capture group — the "#tag"
		start, end := int64(m[2]), int64(m[3])

		// strip trailing punctuation that is not part of the tag
		tag := string(textBytes[start:end])
		tag = strings.TrimRight(tag, ".,!?;:'\")")
		end = start + int64(len(tag))

		if !utf8.ValidString(tag) {
			continue
		}

		// tag value is the text after the leading `#`
		tagVal := tag[1:]
		if tagVal == "" {
			continue
		}

		facets = append(facets, &appbsky.RichtextFacet{
			Index: &appbsky.RichtextFacet_ByteSlice{
				ByteStart: start,
				ByteEnd:   end,
			},
			Features: []*appbsky.RichtextFacet_Features_Elem{
				{
					RichtextFacet_Tag: &appbsky.RichtextFacet_Tag{
						Tag: tagVal,
					},
				},
			},
		})
	}
	return facets
}

// buildFacets returns the full facet slice for text: hashtags plus resolved
// @-mentions, sorted ascending by byte offset. Hashtag and mention tokens cannot
// overlap by construction (one begins with '#', the other with '@'), but the
// official client emits facets in byte order so we sort to match.
func buildFacets(ctx context.Context, text string, resolve handleResolver) []*appbsky.RichtextFacet {
	facets := buildHashtagFacets(text)
	facets = append(facets, buildMentionFacets(ctx, text, resolve)...)

	// Links are detected last: a token already claimed as a hashtag or mention
	// must never also become a link ("@rogersherman.com" stays a mention,
	// "#draftsky.social" stays a tag), so their byte ranges are excluded.
	exclude := make([]ByteRange, len(facets))
	for i, f := range facets {
		exclude[i] = ByteRange{Start: int(f.Index.ByteStart), End: int(f.Index.ByteEnd)}
	}
	facets = append(facets, buildLinkFacets(text, exclude)...)

	sort.Slice(facets, func(i, j int) bool {
		return facets[i].Index.ByteStart < facets[j].Index.ByteStart
	})
	return facets
}

// mentionMatch is a detected @-mention: the UTF-8 byte range of the "@handle"
// token in the source text and the bare handle (no leading '@').
type mentionMatch struct {
	start  int
	end    int
	handle string
}

// detectMentions finds @-mention tokens in text and returns their byte offsets
// and handles. Byte offsets come straight from FindAllStringSubmatchIndex (UTF-8
// byte indices — Gotcha 6), so an emoji earlier in the text does not corrupt them.
// Trailing punctuation is stripped exactly as hashtags strip it.
func detectMentions(text string) []mentionMatch {
	textBytes := []byte(text)
	idxs := mentionRe.FindAllStringSubmatchIndex(text, -1)

	var out []mentionMatch
	for _, m := range idxs {
		// m[2]/m[3] are the byte indices of capture group 1 — the "@handle".
		start, end := m[2], m[3]

		token := string(textBytes[start:end])
		token = strings.TrimRight(token, ".,!?;:'\")")
		end = start + len(token)

		handle := strings.TrimPrefix(token, "@")
		if handle == "" {
			continue
		}
		out = append(out, mentionMatch{start: start, end: end, handle: handle})
	}
	return out
}

// buildMentionFacets detects @-mentions in text, resolves each unique handle to a
// DID concurrently (bounded by the small number of mentions in a post; one lookup
// per unique handle, so the same handle twice costs one resolution), and returns a
// mention facet for every handle that resolved. A handle that fails to resolve
// (NXDOMAIN, deleted account, typo, timeout) is logged at INFO and left as plain
// text — never failing the post, matching Bluesky's own behaviour.
func buildMentionFacets(ctx context.Context, text string, resolve handleResolver) []*appbsky.RichtextFacet {
	matches := detectMentions(text)
	if len(matches) == 0 {
		return nil
	}

	// Per-request dedup: resolve each distinct handle exactly once.
	unique := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		unique[m.handle] = struct{}{}
	}

	type resolution struct {
		handle string
		did    string
		err    error
	}
	results := make(chan resolution, len(unique))
	var wg sync.WaitGroup
	for handle := range unique {
		wg.Add(1)
		go func(handle string) {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, mentionResolveTimeout)
			defer cancel()
			did, err := resolve(rctx, handle)
			results <- resolution{handle: handle, did: did, err: err}
		}(handle)
	}
	wg.Wait()
	close(results)

	dids := make(map[string]string, len(unique))
	for r := range results {
		if r.err != nil {
			slog.InfoContext(ctx, "mention handle did not resolve; left as plain text",
				"handle", r.handle, "error", r.err)
			continue
		}
		dids[r.handle] = r.did
	}

	var facets []*appbsky.RichtextFacet
	for _, m := range matches {
		did, ok := dids[m.handle]
		if !ok {
			continue
		}
		facets = append(facets, &appbsky.RichtextFacet{
			Index: &appbsky.RichtextFacet_ByteSlice{
				ByteStart: int64(m.start),
				ByteEnd:   int64(m.end),
			},
			Features: []*appbsky.RichtextFacet_Features_Elem{
				{
					RichtextFacet_Mention: &appbsky.RichtextFacet_Mention{
						Did: did,
					},
				},
			},
		})
	}
	return facets
}
