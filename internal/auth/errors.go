package auth

import (
	"errors"
	"strings"
)

// ErrDeadSession is a sentinel marking an OAuth session whose tokens are permanently
// unusable: the refresh token was rejected with invalid_grant (a single-use token that
// was already rotated/consumed, revoked, or expired) or the persisted session record is
// gone. Such a session can NEVER recover by retrying or waiting — only a fresh login can.
// It is deliberately distinct from transient network/timeout/5xx failures, which SHOULD
// be retried. Callers may wrap it (%w) to mark a dead session explicitly.
var ErrDeadSession = errors.New("oauth session is dead (re-login required)")

// deadSessionSignals are substrings that, appearing anywhere in an error chain, mean the
// session is definitively dead rather than transiently unavailable.
//
//   - "invalid_grant" — indigo surfaces the OAuth token-endpoint error as the JSON `error`
//     field verbatim (oauth.parseAuthErrorReason), and atclient returns the DoWithAuth
//     error unwrapped, so an invalid_grant on refresh arrives as "...invalid_grant". This
//     is the burned/revoked/expired refresh-token signal.
//   - "oauth session not found" — our auth.PGStore.GetSession miss (pgstore.go). After we
//     delete a dead session's row during cleanup, subsequent requests must still classify
//     as dead (→ re-login prompt), not as a transient error.
//
// String matching is the only signal available: indigo exports no typed error for the
// token-refresh failure (see Gotcha 25). It is centralised here so the fragility lives in
// exactly one place, covered by a test pinned to indigo's actual error format.
var deadSessionSignals = []string{
	"invalid_grant",
	"oauth session not found",
}

// IsDeadSession reports whether err indicates a permanently unusable OAuth session. A dead
// session will never recover by retrying, so the caller should surface a re-login prompt
// (401 / expired notice / redirect to login) rather than a "Bluesky isn't responding"
// notice, and should clear the dead session's cached + stored state. A nil error, a
// timeout, a connection failure, or any 5xx returns false — those are transient.
func IsDeadSession(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDeadSession) {
		return true
	}
	msg := err.Error()
	for _, sig := range deadSessionSignals {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
