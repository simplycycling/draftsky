# DECISIONS.md — DraftSky

The history of significant choices and the paths not taken. CLAUDE.md says what
is; this says why it is, and what was rejected. Append-only. New entries at the
bottom with a date. A decision here is not sacred — but reversing one requires
knowing why it was made.

## Format
**Decision** — context · rejected alternative(s) · revisit-when.
Entries born from incidents are dated by the incident, not the ship date.

---

**PostgreSQL from day one, no SQLite even in dev** — public multi-user app;
dev/prod parity beats convenience. Rejected: SQLite for local dev. Revisit: never.

**Railway over Fly.io** — simplest Go+Postgres deploy for a solo builder;
Fly's multi-region strengths solve problems DraftSky doesn't have. Revisit: if
multi-instance or region-pinning ever matters.

**HTMX + Go templates, no JS framework** — one-language project, no build
step, server-rendered. Rejected: React/Vue/npm toolchain. Revisit: only if the
iOS app's existence makes a shared JS layer genuinely cheaper, which is doubtful.

**DID as canonical identity, never handle** — handles change; DIDs don't.
Rejected: handle-keyed users. Revisit: never.

**API-first: JSON endpoints exist before HTMX pages, and stay HTML-free** —
the JSON API is the future iOS contract. Revisit: never; this is load-bearing.

**Thread view navigates, doesn't nest** — clicking a reply re-focuses the
thread on it instead of rendering nested trees. Simpler to build and read; no
information unreachable. Rejected: Bluesky-style deep nesting. Revisit: if user
feedback shows the extra click genuinely hurts.

**204 vs HTMX: split on HX-Request header** — HTMX 2.x treats 204 as no-swap,
but 204 is the correct REST answer; the API contract is never degraded to suit
the web layer. Rejected: returning 200 everywhere. (see the HX-Request branch
comment in internal/handlers/templates.go)

**hls.js self-hosted, no CDN** — unpkg was a reliability and Brave-Shields
liability for a load-bearing script. Rejected: third-party CDN. Revisit: never
reintroduce a CDN for load-bearing JS.

**Quoted-post media is thumbnail-only; playback via click-through to the
quoted thread** — avoids nested players inside clickable cards. Revisit: if
users report the extra hop as friction (filed post-GA with GIF work).

**Mute/block inheritance shipped "contained" (drop, don't collapse)** —
viewer flags + muted words at the one chokepoint; collapse-with-reveal UI,
interstitials, and mute write-paths deferred. Rejected for v1: full parity.
Revisit: post-GA polish item, on the roadmap.

**Free-tier cap has a documented, unfixed count-then-insert race** — worst
case is one bonus template; a constraint isn't worth it at this scale.
Revisit: if template counts ever matter commercially.

**Monolith forever (approximately)** — one-person project; heavy lifting is
already Bluesky's distributed system; scaling path is vertical → debt items
(Redis for rate-limit/caches) → horizontal copies of the same binary. Rejected:
microservices on a growth trigger. Revisit: only for a component with a
genuinely different scaling/deploy/failure profile (analytics worker is the
one candidate).

**iOS monetisation leaning IAP-only, ads under doubt** — ads drag in ATT
prompts, SDK weight, and review complexity; contrary to the original
ads+IAP plan in CLAUDE.md. Not final. Revisit: at iOS build time.

**Scheduled posts = Premium; polls = free** — scheduling monetises workflows
(users with budgets); polls acquire users through feeds (every shared poll is
an ad with a login prompt). Revisit: pricing review post-launch.

**Polls: votes on our infrastructure = our first real data liability** —
poll.blue's shutdown killed every historical poll link. Accepted knowingly;
schema and retention deserve explicit design when built. Optional lexicon
(social.draftsky.*) records for public votes considered attractive, undecided.

**Polls ship unannounced** — no teasing; a surprise release is its own second
launch. Revisit: never; this is doctrine now.

**October deadline is split-tier** — web feature-complete + Premium is a
commitment; iOS is a stretch (TestFlight by puck drop acceptable). Apple's
review queue is not ours to compress. Revisit: September reality check.

**Analytics engagement-snapshot collector ships early, UI ships late** — the
paid flagship needs weeks of collected data to show anything at launch; data
not collected is gone forever (same reasoning as last_seen_at).

**2026-07-16: async shell over synchronous page builds** — GET / renders with
zero upstream calls, structurally (buildShellLayout takes no feed client);
regions lazy-load with honest failure notices. Rejected: keeping synchronous
renders with longer timeouts. Born from the 2026-07-15 PDS degradation incident
(TLS handshake timeouts against inkcap.us-east.host.bsky.network - see Railway logs).

**2026-07-17: shared ClientSession per sessionID over per-call instances** —
Gotcha 24's mutex failed because refresh happens reactively inside DoWithAuth,
not in ResumeSession; concurrent distinct instances clobbered the single-use
refresh token (indigo documents this). Rejected: serializing XRPC per session
(would refund the async-shell concurrency win); rejected: documenting without
curing (users randomly logged out during every Bluesky brown-out). The cache
is accepted sync.Map-class debt. See Gotcha 25.

---

## Backfilled foundational decisions

Surfaced from CLAUDE.md's Key Architectural Decisions during the 2026-07-18
memory review — original choices the seed did not enumerate. Undated because
they predate this file; they are foundational, not recent.

**AT Protocol OAuth 2.0 PKCE over app passwords** — multi-user public app;
each user authenticates through their own PDS, tokens are per-user and
revocable, no shared secret held by DraftSky. Rejected: app passwords
(shared-secret, no per-user scoping, discouraged for multi-user clients).
Revisit: never.

**sqlc (type-safe Go from raw SQL) over an ORM** — raw SQL stays readable and
reviewable; generated Go is compile-time type-checked against the schema; no
runtime query-builder indirection. Rejected: an ORM; hand-written database/sql
boilerplate. Revisit: never.

**PostgreSQL-backed OAuth store over indigo's in-memory MemStore** — PKCE
verifiers, pending auth requests, and rotating tokens must survive server
restarts (Railway redeploys frequently); an in-memory store would sign every
user out on each deploy. Rejected: MemStore. Revisit: never.

**Admin dashboard 404s for every non-owner, not 403/redirect** — a 403 or a
redirect advertises that /admin/stats exists; a bare 404 makes it
indistinguishable from any unknown path, hiding the route entirely. Rejected:
403/redirect gating. Revisit: never.

**Post facets built via indigo helpers, never string concatenation** —
hashtags, mentions, and links are byte-range annotations at UTF-8 byte offsets;
hand-rolled string building corrupts those offsets the moment emoji or other
multibyte content appears. Rejected: naive string concatenation. Revisit: never.
