package auth

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsDeadSession classifies the errors the feed layer actually surfaces. The dead-session
// cases (invalid_grant, missing session record) mean re-login is the only recovery; the
// transient cases (timeout, 5xx, connection reset) must NOT be classified dead, or a PDS
// hiccup would spuriously log users out.
func TestIsDeadSession(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// The exact shape produced live: our feed wrap → indigo DoWithAuth wrap →
			// RefreshTokens → parseAuthErrorReason returns the JSON `error` field verbatim.
			// atclient returns the DoWithAuth error unwrapped (apiclient.go), so the string
			// survives to us. This pins the classifier to indigo's real format.
			"invalid_grant refresh failure (real indigo shape)",
			fmt.Errorf("getTimeline: %w", fmt.Errorf("failed to refresh OAuth tokens: %w",
				errors.New("token refresh failed (HTTP 400): invalid_grant"))),
			true,
		},
		{
			"session record gone after cleanup",
			fmt.Errorf("resume session: %w",
				errors.New("oauth session not found (did=did:plc:x sid=abc): no rows in result set")),
			true,
		},
		{
			"explicit ErrDeadSession wrap",
			fmt.Errorf("something: %w", ErrDeadSession),
			true,
		},
		{"context deadline (timeout)", errors.New("getTimeline: context deadline exceeded"), false},
		{"upstream 502", errors.New("getTimeline: API request failed (HTTP 502)"), false},
		{"connection reset", errors.New("getTimeline: read tcp: connection reset by peer"), false},
		{"generic upstream 500", errors.New("getTimeline: API request failed (HTTP 500): InternalServerError"), false},
		// An expired ACCESS token that refreshes fine is not a dead session — it never
		// surfaces as an error at all, but guard against a false match on the substring.
		{"unrelated error mentioning grant", errors.New("failed to grant permission"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDeadSession(tc.err); got != tc.want {
				t.Errorf("IsDeadSession(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
