package db

// Integration tests for the activity-instrumentation queries (TouchUserLastSeen,
// GetAdminStats, GetContentStats). These exercise real SQL — the once-per-hour
// staleness gate and the FILTER-aggregate windows can only be verified against a live
// PostgreSQL, not a fake (see CLAUDE.md Gotcha 16).
//
// They run against the dev database named by DATABASE_URL and SKIP when it is unset or
// unreachable, so the default `go test ./...` on a machine without a DB stays green.
//
// Gotcha 19: every row these tests write is created with a unique DID and torn down by
// the integer id captured at creation time — never by handle/DID/name — so a developer's
// real rows are never at risk. User deletes cascade to templates and post_history.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool connects to the dev DB, or skips the test if DATABASE_URL is unset or the
// database is unreachable.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot create pool (%v) — skipping DB integration test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("cannot reach database (%v) — skipping DB integration test", err)
	}
	// Register the close via Cleanup (not defer) so it runs AFTER the row teardowns:
	// t.Cleanup callbacks run LIFO, and this is registered first, so it fires last —
	// a plain defer here would close the pool before the DELETE cleanups could run.
	t.Cleanup(pool.Close)
	return pool
}

// insertTestUser creates a user with a controlled created_at/last_seen_at and returns
// its id. last_seen is nil-able so NULL cases can be exercised. Registers id-scoped
// teardown (cascades to templates/post_history).
func insertTestUser(t *testing.T, pool *pgxpool.Pool, did string, createdAt time.Time, lastSeen *time.Time) int32 {
	t.Helper()
	var ls interface{}
	if lastSeen != nil {
		ls = *lastSeen
	}
	var id int32
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (did, handle, created_at, last_seen_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		did, did, createdAt, ls,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup user id=%d: %v", id, err)
		}
	})
	return id
}

// getLastSeen reads last_seen_at for a specific id.
func getLastSeen(t *testing.T, pool *pgxpool.Pool, id int32) pgtype.Timestamptz {
	t.Helper()
	var ls pgtype.Timestamptz
	if err := pool.QueryRow(context.Background(),
		`SELECT last_seen_at FROM users WHERE id = $1`, id).Scan(&ls); err != nil {
		t.Fatalf("read last_seen_at for id=%d: %v", id, err)
	}
	return ls
}

func uniqueDID(tag string) string {
	return fmt.Sprintf("did:plc:dstest-%s-%d", tag, time.Now().UnixNano())
}

func TestTouchUserLastSeen_FreshValueNoWrite(t *testing.T) {
	pool := newTestPool(t)
	q := New(pool)

	// A last_seen 5 minutes ago is "fresh" (< 1h) — the staleness gate must NOT write.
	fresh := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Microsecond)
	did := uniqueDID("fresh")
	id := insertTestUser(t, pool, did, time.Now(), &fresh)

	if err := q.TouchUserLastSeen(context.Background(), did); err != nil {
		t.Fatalf("TouchUserLastSeen: %v", err)
	}

	got := getLastSeen(t, pool, id)
	if !got.Valid {
		t.Fatal("last_seen_at became NULL — unexpected")
	}
	if !got.Time.Equal(fresh) {
		t.Errorf("fresh value should be untouched: want %v, got %v", fresh, got.Time)
	}
}

func TestTouchUserLastSeen_StaleValueWrites(t *testing.T) {
	pool := newTestPool(t)
	q := New(pool)

	// 2 hours stale (> 1h) — the gate must write, advancing last_seen to ~now.
	stale := time.Now().Add(-2 * time.Hour).UTC()
	did := uniqueDID("stale")
	id := insertTestUser(t, pool, did, time.Now(), &stale)

	if err := q.TouchUserLastSeen(context.Background(), did); err != nil {
		t.Fatalf("TouchUserLastSeen: %v", err)
	}

	got := getLastSeen(t, pool, id)
	if !got.Valid {
		t.Fatal("last_seen_at is NULL after touch — expected a write")
	}
	if time.Since(got.Time) > time.Minute {
		t.Errorf("stale value should have advanced to ~now, got %v (%.0fs ago)", got.Time, time.Since(got.Time).Seconds())
	}
}

func TestTouchUserLastSeen_NullValueWrites(t *testing.T) {
	pool := newTestPool(t)
	q := New(pool)

	// NULL last_seen (a row that predates instrumentation) must be backfilled.
	did := uniqueDID("null")
	id := insertTestUser(t, pool, did, time.Now(), nil)

	if err := q.TouchUserLastSeen(context.Background(), did); err != nil {
		t.Fatalf("TouchUserLastSeen: %v", err)
	}

	got := getLastSeen(t, pool, id)
	if !got.Valid {
		t.Fatal("last_seen_at still NULL after touch — expected a write")
	}
	if time.Since(got.Time) > time.Minute {
		t.Errorf("backfilled value should be ~now, got %v", got.Time)
	}
}

func TestGetAdminStats_ShapeAndSeededDeltas(t *testing.T) {
	pool := newTestPool(t)
	q := New(pool)
	ctx := context.Background()

	// Shape on the live (non-empty) DB: counts are non-negative and internally ordered.
	base, err := q.GetAdminStats(ctx)
	if err != nil {
		t.Fatalf("GetAdminStats baseline: %v", err)
	}
	if base.TotalUsers < 0 || base.Dau < 0 || base.Mau < 0 {
		t.Fatalf("counts must be non-negative, got %+v", base)
	}

	// Seed three users with controlled created_at/last_seen_at and assert the exact
	// deltas. The delta approach is immune to whatever else is in the shared DB.
	now := time.Now()
	d3 := now.Add(-3 * 24 * time.Hour)   // within week/30d, outside "today"
	d40 := now.Add(-40 * 24 * time.Hour) // outside every window
	t3 := d3
	t40 := d40
	tnow := now
	insertTestUser(t, pool, uniqueDID("stats-now"), now, &tnow)
	insertTestUser(t, pool, uniqueDID("stats-3d"), d3, &t3)
	insertTestUser(t, pool, uniqueDID("stats-40d"), d40, &t40)

	after, err := q.GetAdminStats(ctx)
	if err != nil {
		t.Fatalf("GetAdminStats after seed: %v", err)
	}

	checks := []struct {
		name      string
		got, want int64
	}{
		{"total_users", after.TotalUsers - base.TotalUsers, 3},
		{"new_today", after.NewToday - base.NewToday, 1},
		{"new_this_week", after.NewThisWeek - base.NewThisWeek, 2},
		{"dau", after.Dau - base.Dau, 1},
		{"wau", after.Wau - base.Wau, 2},
		{"mau", after.Mau - base.Mau, 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s delta: want %d, got %d", c.name, c.want, c.got)
		}
	}
}

func TestGetContentStats_SeededDeltas(t *testing.T) {
	pool := newTestPool(t)
	q := New(pool)
	ctx := context.Background()

	base, err := q.GetContentStats(ctx)
	if err != nil {
		t.Fatalf("GetContentStats baseline: %v", err)
	}
	if base.TotalTemplates < 0 || base.TotalPosts < 0 {
		t.Fatalf("counts must be non-negative, got %+v", base)
	}

	now := time.Now()
	uid := insertTestUser(t, pool, uniqueDID("content"), now, &now)

	// One template and one post_history row owned by our test user. Both cascade-delete
	// when the user is torn down, so no separate cleanup is needed.
	if _, err := pool.Exec(ctx,
		`INSERT INTO templates (user_id, name, suffix) VALUES ($1, $2, $3)`,
		uid, "dstest-template", "#dstest"); err != nil {
		t.Fatalf("insert template: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO post_history (user_id, uri, hashtags) VALUES ($1, $2, $3)`,
		uid, "at://dstest/app.bsky.feed.post/1", []string{"dstest"}); err != nil {
		t.Fatalf("insert post_history: %v", err)
	}

	after, err := q.GetContentStats(ctx)
	if err != nil {
		t.Fatalf("GetContentStats after seed: %v", err)
	}
	if d := after.TotalTemplates - base.TotalTemplates; d != 1 {
		t.Errorf("total_templates delta: want 1, got %d", d)
	}
	if d := after.TotalPosts - base.TotalPosts; d != 1 {
		t.Errorf("total_posts delta: want 1, got %d", d)
	}
}
