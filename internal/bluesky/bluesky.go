package bluesky

import (
	"context"
	"fmt"
	"regexp"
	"strings"
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

// Post submits a post to Bluesky on behalf of the user identified by did/sessionID.
// If suffix is non-empty it is appended to text with a space separator.
// Hashtags in the final text are detected and annotated as richtext facets.
func (p *Poster) Post(ctx context.Context, didStr, sessionID, text, suffix string) (*PostResult, error) {
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
		trimmed := strings.TrimRight(text, " ")
		if strings.HasSuffix(trimmed, "\n") {
			fullText = trimmed + suffix
		} else {
			fullText = trimmed + " " + suffix
		}
	}
	fullText = strings.TrimSpace(fullText)

	facets := buildHashtagFacets(fullText)

	post := &appbsky.FeedPost{
		Text:      fullText,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Facets:    facets,
	}

	apiClient := sess.APIClient()
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
