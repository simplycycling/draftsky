package feed

import (
	"strings"
	"time"
	"unicode"

	appbsky "github.com/bluesky-social/indigo/api/bsky"

	"github.com/rsherman/draftsky/internal/bluesky"
)

// MutedWord is DraftSky's representation of an app.bsky.actor.defs#mutedWord from the
// user's Bluesky preferences (app.bsky.actor.getPreferences → mutedWordsPref).
//
//   - Targets: "content" and/or "tag" — what part of a post the word applies to.
//   - ActorTarget: "" or "all" (apply to everyone) | "exclude-following" (spare accounts
//     the viewer follows).
//   - ExpiresAt: RFC3339 timestamp after which the mute no longer applies; "" = never.
type MutedWord struct {
	Value       string
	Targets     []string
	ActorTarget string
	ExpiresAt   string
}

// authorHidden reports whether a post (or repost) whose author carries viewer state v
// should be dropped from a feed. It is true when the viewer has muted the author (directly
// or via a mute list), the author blocks the viewer (blockedBy), or the viewer blocks the
// author (blocking is the AT URI of the viewer's block record, or blockingByList). v may
// be nil (no viewer state → not hidden).
//
// This is the account-level moderation drop; muted *words* are handled separately by
// matchesMutedWords. v1 drops the post entirely (roadmap item 9 — collapse-with-reveal is
// post-GA).
func authorHidden(v *appbsky.ActorDefs_ViewerState) bool {
	if v == nil {
		return false
	}
	if v.Muted != nil && *v.Muted {
		return true
	}
	if v.MutedByList != nil {
		return true
	}
	if v.BlockedBy != nil && *v.BlockedBy {
		return true
	}
	if v.Blocking != nil && *v.Blocking != "" {
		return true
	}
	if v.BlockingByList != nil {
		return true
	}
	return false
}

// matchesMutedWords reports whether a post with the given text and hashtags is hidden by
// any active muted word. following is whether the viewer follows the post's author (for
// actorTarget "exclude-following"); now is the reference time used for expiry.
//
// Semantics follow the app.bsky.actor.defs#mutedWord lexicon: a "tag"-target word matches
// the post's hashtags (case-insensitive, '#' insensitive); a "content"-target word matches
// the post text. Expired words (expiresAt in the past) and, when following is true,
// exclude-following words are skipped.
//
// Simplifications vs Bluesky's reference client (deliberate, contained pre-GA scope):
//   - Content matching tokenizes on Unicode letter/digit boundaries and compares whole
//     tokens case-insensitively, so "cat" never matches "catalogue". A multi-word muted
//     value is matched as a case-insensitive substring (phrase).
//   - We do NOT strip diacritics, expand possessives/plurals, or special-case URLs, domains,
//     or per-language tokenization the way the official implementation does.
//   - "tag" matching uses the hashtags parsed from the post text (bluesky.ExtractHashtags),
//     not the post record's non-inline `tags` field.
func matchesMutedWords(words []MutedWord, text string, hashtags []string, following bool, now time.Time) bool {
	if len(words) == 0 {
		return false
	}

	lowerText := strings.ToLower(text)

	tagSet := make(map[string]struct{}, len(hashtags))
	for _, h := range hashtags {
		tagSet[strings.ToLower(strings.TrimPrefix(h, "#"))] = struct{}{}
	}

	var tokens map[string]struct{} // lazily built from lowerText for content matching

	for _, w := range words {
		val := strings.ToLower(strings.TrimSpace(w.Value))
		if val == "" {
			continue
		}
		if w.ExpiresAt != "" {
			if exp, err := time.Parse(time.RFC3339, w.ExpiresAt); err == nil && !exp.After(now) {
				continue // expired
			}
		}
		if w.ActorTarget == "exclude-following" && following {
			continue
		}

		var hasContent, hasTag bool
		for _, t := range w.Targets {
			switch t {
			case "content":
				hasContent = true
			case "tag":
				hasTag = true
			}
		}

		if hasTag {
			if _, ok := tagSet[strings.TrimPrefix(val, "#")]; ok {
				return true
			}
		}
		if hasContent {
			if strings.ContainsAny(val, " \t\n") {
				if strings.Contains(lowerText, val) {
					return true
				}
			} else {
				if tokens == nil {
					tokens = tokenizeWords(lowerText)
				}
				if _, ok := tokens[val]; ok {
					return true
				}
			}
		}
	}
	return false
}

// tokenizeWords splits already-lowercased text into a set of whole words, breaking on any
// rune that is not a Unicode letter or digit. Used for whole-word "content" mute matching.
func tokenizeWords(lowerText string) map[string]struct{} {
	fields := strings.FieldsFunc(lowerText, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		set[f] = struct{}{}
	}
	return set
}

// authorFollowed reports whether the viewer follows the account behind viewer state v
// (its Following field is the AT URI of the viewer's follow record). Used to honour a
// muted word's actorTarget "exclude-following".
func authorFollowed(v *appbsky.ActorDefs_ViewerState) bool {
	return v != nil && v.Following != nil && *v.Following != ""
}

// postHiddenByMutedWords reports whether pv (already mapped) should be dropped by muted
// words, given the author's viewer state. It parses the post's inline hashtags itself.
func postHiddenByMutedWords(words []MutedWord, pv PostView, authorViewer *appbsky.ActorDefs_ViewerState, now time.Time) bool {
	if len(words) == 0 {
		return false
	}
	return matchesMutedWords(words, pv.Text, bluesky.ExtractHashtags(pv.Text), authorFollowed(authorViewer), now)
}
