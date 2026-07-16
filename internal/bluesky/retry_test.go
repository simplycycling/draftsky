package bluesky

import (
	"context"
	"errors"
	"fmt"
	"testing"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/atproto/atclient"
)

func TestIsRetriablePostError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not retriable", nil, false},
		{"400 bad request is not retriable", &atclient.APIError{StatusCode: 400}, false},
		{"401 unauthorized is not retriable", &atclient.APIError{StatusCode: 401}, false},
		{"409 conflict is not retriable", &atclient.APIError{StatusCode: 409}, false},
		{"429 rate limit is not retriable", &atclient.APIError{StatusCode: 429}, false},
		{"500 is retriable", &atclient.APIError{StatusCode: 500}, true},
		{"502 bad gateway is retriable", &atclient.APIError{StatusCode: 502}, true},
		{"503 unavailable is retriable", &atclient.APIError{StatusCode: 503}, true},
		{"wrapped 400 is not retriable", fmt.Errorf("create record: %w", &atclient.APIError{StatusCode: 400}), false},
		{"wrapped 503 is retriable", fmt.Errorf("create record: %w", &atclient.APIError{StatusCode: 503}), true},
		{"plain network error is retriable", errors.New("dial tcp: i/o timeout"), true},
		{"context deadline is retriable", context.DeadlineExceeded, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriablePostError(tt.err); got != tt.want {
				t.Errorf("isRetriablePostError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCreateWithRetry(t *testing.T) {
	ok := &comatproto.RepoCreateRecord_Output{Uri: "at://did:plc:x/app.bsky.feed.post/1", Cid: "bafy"}
	transient := errors.New("connection refused")
	rejected := &atclient.APIError{StatusCode: 400}

	t.Run("succeeds on first attempt without retrying", func(t *testing.T) {
		calls := 0
		out, err := createWithRetry(context.Background(), 0, func(context.Context) (*comatproto.RepoCreateRecord_Output, error) {
			calls++
			return ok, nil
		})
		if err != nil || out != ok {
			t.Fatalf("got (%v, %v), want (%v, nil)", out, err, ok)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("retries once then succeeds", func(t *testing.T) {
		calls := 0
		out, err := createWithRetry(context.Background(), 0, func(context.Context) (*comatproto.RepoCreateRecord_Output, error) {
			calls++
			if calls == 1 {
				return nil, transient
			}
			return ok, nil
		})
		if err != nil || out != ok {
			t.Fatalf("got (%v, %v), want (%v, nil)", out, err, ok)
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2", calls)
		}
	})

	t.Run("gives up after one retry", func(t *testing.T) {
		calls := 0
		_, err := createWithRetry(context.Background(), 0, func(context.Context) (*comatproto.RepoCreateRecord_Output, error) {
			calls++
			return nil, transient
		})
		if err == nil {
			t.Fatal("expected an error after exhausting the retry")
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2 (initial + one retry)", calls)
		}
	})

	t.Run("never retries a 4xx rejection (double-post guard)", func(t *testing.T) {
		calls := 0
		_, err := createWithRetry(context.Background(), 0, func(context.Context) (*comatproto.RepoCreateRecord_Output, error) {
			calls++
			return nil, rejected
		})
		if err == nil {
			t.Fatal("expected the rejection error to be returned")
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 — a rejected record must never be retried", calls)
		}
	})

	t.Run("aborts the delay when context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, err := createWithRetry(ctx, 10_000_000_000 /* 10s; must not actually elapse */, func(context.Context) (*comatproto.RepoCreateRecord_Output, error) {
			calls++
			return nil, transient
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 — the retry should be aborted by cancellation", calls)
		}
	})
}
