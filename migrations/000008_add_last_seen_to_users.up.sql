-- last_seen_at powers activity metrics (DAU/WAU/MAU) on the admin stats page.
-- Nullable and NO default: existing rows genuinely have no activity data — we must
-- not fabricate a "seen" time for them. The auth middleware backfills it on next visit.
ALTER TABLE users ADD COLUMN last_seen_at TIMESTAMPTZ;
