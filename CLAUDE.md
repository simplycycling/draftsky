# DraftSky — CLAUDE.md

## Project Overview

DraftSky is a multi-user Bluesky client built around template-based posting, with an
integrated feed. Users authenticate via Bluesky OAuth, create named templates
(pre-composed hashtag sets and recurring text), and select a template when composing a
post. The template's suffix is appended to their post before it is submitted to Bluesky
via the AT Protocol.

The default view is the user's Following feed. After a post is submitted, DraftSky
automatically switches to a merged hashtag feed — a combined, recency-sorted stream of
all hashtags used in that post — so the user can immediately see the conversation they
have just entered. The Following feed is always accessible to return to.

DraftSky is live in private beta at https://www.draftsky.social. The long-term goal is
a complete Bluesky client replacement on web and iOS. The primary motivation is
reducing repetitive hashtag entry for topic-specific posting (sports coverage, hobby
communities, professional topics, project promotion).

---

## Session protocol

Standard opening for every working session:

- Read CLAUDE.md thoroughly — including the Gotchas and the Definition of Done.
- Read DECISIONS.md — why things are the way they are, and what was rejected.
- One concern per session; don't widen scope mid-session.
- Respect scope fences — additive edits over rewrites, and confirm before reversing a
  documented decision.

---

## Tech Stack

| Layer        | Technology                                      |
|--------------|-------------------------------------------------|
| Backend      | Go, Gin                                         |
| Database     | PostgreSQL (pgx/v5 driver)                      |
| Query layer  | sqlc (type-safe Go from raw SQL)                |
| Migrations   | golang-migrate                                  |
| Web UI       | Go html/template + HTMX                         |
| Auth         | AT Protocol OAuth 2.0 (PKCE)                    |
| Bluesky API  | github.com/bluesky-social/indigo                |
| Deployment   | Railway (Go binary + managed PostgreSQL)        |
| iOS (future) | SwiftUI, hitting the same JSON API              |

---

## Architecture

DraftSky is an API-first application. The Go backend serves both the HTMX-driven web UI
and a JSON API. The iOS app (future phase) will consume the same JSON API endpoints.

```
┌─────────────────────────────────────────┐
│            Go (Gin) Backend             │
│                                         │
│  /auth/*         AT Protocol OAuth      │
│  /api/templates  Template CRUD (JSON)   │
│  /api/post       Compose + post (JSON)  │
│  /api/like       Like/unlike (JSON)     │
│  /api/feed       Following + hashtag    │
│  /feed/*         HTMX feed partials     │
│  /*              HTMX web UI            │
└───────────────┬─────────────────────────┘
                │ same /api/* endpoints
      ┌─────────┴──────────┐
      │                    │
  HTMX Web UI          iOS App
  (Go templates)       (SwiftUI, future)
```

The web UI is server-side rendered using Go's `html/template` package. HTMX handles
dynamic interactions (template CRUD without full page reloads, feed swaps, likes,
infinite scroll). There is no separate frontend build step and no JavaScript framework.
JSON API handlers and HTMX partial handlers are kept separate — the JSON API stays pure
for the future iOS app.

---

## Key Architectural Decisions

### PostgreSQL over SQLite
This is a public multi-user application. PostgreSQL is the production database from day
one. Do not suggest SQLite, even for development — use the local Docker PostgreSQL
instance (`draftsky-dev-db`) to keep parity.

### sqlc for database access
All database queries are written as raw SQL in `/internal/db/queries/`. sqlc generates
type-safe Go code from these queries. Do not use an ORM. Do not write manual
`database/sql` query boilerplate. When adding a new query, add it to the appropriate
`.sql` file and re-run `sqlc generate`.
**Note:** `sqlc.yaml` schema must point at `.up.sql` migration files only — never at the
migrations directory as a whole.

### AT Protocol OAuth (not app passwords)
DraftSky is multi-user and public. Auth uses the AT Protocol OAuth 2.0 PKCE flow, not
app passwords. Each user authenticates through their own PDS (Personal Data Server).
The user's DID (Decentralised Identifier) is the canonical user identifier in the
database — not their handle, which can change. OAuth client state (PKCE verifiers,
pending sessions, tokens) is stored in a PostgreSQL-backed ClientAuthStore
(`internal/auth/pgstore.go`, tables `oauth_sessions` and `oauth_auth_requests`) so it
survives server restarts.

### HTMX over a JS framework
The web UI is intentionally kept in Go-land. HTMX attributes drive dynamic behaviour.
Do not introduce React, Vue, or any npm-based frontend toolchain. Vanilla JS is
acceptable for small enhancements and lives in `/static/app.js`.

### Bluesky posts use facets
Hashtags and mentions in Bluesky are not plain text — they are `facets` in the
`app.bsky.feed.post` lexicon (rich text byte-range annotations). Always construct posts
using the indigo library and the existing helpers in `internal/bluesky/bluesky.go`
(`buildFacets` — hashtags + mentions, byte-sorted; `ExtractHashtags`), never by naive
string concatenation. Byte offsets are UTF-8 byte positions, not rune positions.
Mentions: boundary-anchored regex (the `@` must begin the token, so emails never
match), concurrent handle→DID resolution (3s timeout, per-request dedup), unresolvable
handles stay plain text (INFO log, never fail the post). Facet detection runs on the
combined body + template suffix, so suffix mentions work (collab templates).

### Replies use AT Protocol threading
Replies require a `reply` object with both `root` and `parent` `{uri, cid}` StrongRefs.
For a reply to a top-level post, root == parent. For a reply to a reply, root must be
the original thread root — `PostView` carries `ReplyRootURI`/`ReplyRootCID` from each
post's own `reply.root` so the composer threads correctly. Reply fields on
`POST /api/post` are all-or-nothing (400 if partially supplied).

### Feed behaviour
The default feed is the user's Following feed (`app.bsky.feed.getTimeline`). After a
post is successfully submitted, the UI automatically switches to a merged hashtag feed
built by querying `app.bsky.feed.searchPosts` for each hashtag concurrently, merging
server-side, deduplicating by URI, and sorting by `indexedAt` descending. Replies
without hashtags refresh the current feed instead (tracked via `currentFeedURL` in
app.js). Clicking a recent tag in the right rail triggers the same HTMX feed switch via
`switchToHashtagFeed()`.

All feed requests are authenticated under the user's OAuth session (via
`resumeAPIClient`), so viewer state (`LikedByMe`, `LikeURI`) is populated.

`PostView` includes: author (DID, handle, display name, avatar), text, counts,
`LikedByMe`/`LikeURI`, `Images []PostImage` (from `app.bsky.embed.images#view`),
`ExternalLink *PostExternalLink` (from `app.bsky.embed.external#view`), and
`ReplyRootURI`/`ReplyRootCID`.

---

## Database Schema

```sql
CREATE TABLE users (
    id            SERIAL PRIMARY KEY,
    did           TEXT UNIQUE NOT NULL,   -- e.g. did:plc:abc123 (canonical identifier)
    handle        TEXT,                   -- e.g. roger.bsky.social (may change)
    avatar        TEXT,                   -- avatar URL fetched at login via getProfile
    access_token  TEXT,
    refresh_token TEXT,
    token_expiry  TIMESTAMPTZ,
    plan          TEXT NOT NULL DEFAULT 'free', -- 'free' | 'paid'; set to 'paid' on verified IAP
    theme         TEXT NOT NULL DEFAULT 'ocean', -- see Themes; non-default is paid-only
    last_seen_at  TIMESTAMPTZ,             -- activity stamp for DAU/WAU/MAU; NULL until first visit
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE post_history (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    uri        TEXT NOT NULL,              -- AT URI of the submitted post
    hashtags   TEXT[] NOT NULL,            -- extracted at post time
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE templates (
    id         SERIAL PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,             -- e.g. "Devils Game"; max 100 runes (validated)
    suffix     TEXT NOT NULL,             -- e.g. "#NJDevils #NHL"; max 250 runes (validated)
    position   INTEGER DEFAULT 0,         -- display order in dropdown
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, name)
);

-- oauth_sessions and oauth_auth_requests back the PostgreSQL ClientAuthStore
```

Applied migrations: 000001_create_users, 000002_create_templates, 000003_add_plan_to_users,
000004_create_post_history, 000005_add_theme_to_users, 000006_add_avatar_to_users,
000007_create_oauth_store, 000008_add_last_seen_to_users.

---

## API Endpoints

### Auth
| Method | Path                   | Description                          |
|--------|------------------------|--------------------------------------|
| GET    | /auth/login            | Initiate AT Protocol OAuth PKCE flow |
| GET    | /auth/callback         | OAuth callback, exchange code        |
| POST   | /auth/logout           | Clear session                        |
| GET    | /client-metadata.json  | OAuth client metadata                |

### Templates (JSON API, RequireAuth + operations rate limit)
| Method | Path                   | Description                              |
|--------|------------------------|------------------------------------------|
| GET    | /api/templates         | List user's templates                    |
| POST   | /api/templates         | Create (name ≤100, suffix ≤250 runes)    |
| PUT    | /api/templates/:id     | Update (same validation, ownership check)|
| DELETE | /api/templates/:id     | Delete (ownership check)                 |
| PUT    | /api/templates/reorder | Update display order (transactional)     |
| GET    | /api/composer/templates| Templates for composer dropdown          |

### Post (JSON API, RequireAuth + post rate limit 10/min)
| Method | Path       | Description                                              |
|--------|------------|----------------------------------------------------------|
| POST   | /api/post  | Compose + submit. Body: text, template_id (optional),    |
|        |            | reply_parent_uri/cid + reply_root_uri/cid (all-or-none)  |

### Likes (JSON API, RequireAuth + operations rate limit)
| Method | Path       | Description                                  |
|--------|------------|----------------------------------------------|
| POST   | /api/like  | Like a post. Form data: uri, cid             |
| DELETE | /api/like  | Unlike. Form/query: like_uri + post refs     |

### Feed (JSON API, RequireAuth)
| Method | Path                  | Description                                     |
|--------|-----------------------|-------------------------------------------------|
| GET    | /api/feed/following   | Following feed (cursor-paginated)               |
| GET    | /api/feed/hashtags    | Merged hashtag feed; `tags` query param         |
| GET    | /api/feed/recent-tags | Last 10 unique hashtags posted by the user      |

### Web UI (RequireSession — redirects to /login; HTMX partials)
| Method | Path             | Description                                     |
|--------|------------------|-------------------------------------------------|
| GET    | /                | Instant shell; feed/tabs/tags lazy-load on load |
| GET    | /feed/tabs       | HTMX lazy partial — saved-feeds tab bar         |
| GET    | /feed/recent-tags| HTMX lazy partial — right-rail recent tags      |
| GET    | /feed/following  | HTMX partial — Following feed                   |
| GET    | /feed/hashtags   | HTMX partial — merged hashtag feed              |
| GET    | /templates       | Template management page                        |
| GET    | /settings        | Settings (page not yet built — priority item)   |
| GET    | /login           | Login page (public; bounces if authed)          |

### Admin (owner-only; RequireAdmin gates on ADMIN_DID)
| Method | Path          | Description                                                    |
|--------|---------------|----------------------------------------------------------------|
| GET    | /admin/stats  | Owner dashboard: user totals, new today/week, DAU/WAU/MAU,     |
|        |               | template + post counts. 404 (not 403/redirect) for any non-   |
|        |               | owner or missing session — the route's existence is hidden.   |

### Infrastructure
| Method | Path          | Description                                        |
|--------|---------------|----------------------------------------------------|
| GET    | /health       | Health check                                       |
| GET    | /robots.txt   | Allows / and /login; disallows app routes          |
| GET    | /favicon.svg  | SVG favicon (Deep Ocean palette); also /favicon.ico|

A bare-domain middleware 301-redirects `draftsky.social` → `https://www.draftsky.social`
(registered first on the engine).

---

## Project Structure

```
draftsky/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── auth/          # AT Protocol OAuth handlers, session cookies, pgstore.go
│   ├── bluesky/       # indigo wrapper: Post (with ReplyRefs), facets, ExtractHashtags
│   ├── feed/          # Following + hashtag feed clients, PostView mapping
│   ├── db/
│   │   ├── queries/   # .sql files (sqlc source)
│   │   └── sqlc/      # generated Go code (do not edit manually)
│   ├── handlers/      # Gin route handlers (JSON API + HTMX UI handlers)
│   ├── middleware/    # RequireAuth (401 JSON), RequireSession (redirect),
│   │                  # SecurityHeaders, rate limiters
│   └── models/        # Shared types not generated by sqlc
├── migrations/        # golang-migrate SQL migration files
├── templates/         # Go html/template files (.html)
│   └── partials/      # HTMX partials (feed, composer, feed_controls, ...)
├── static/            # style.css, app.js, favicon.svg
├── db/
│   └── sqlc.yaml      # sqlc configuration
├── Dockerfile         # multi-stage, CGO_ENABLED=0, bookworm-slim + ca-certificates
├── railway.toml
├── CLAUDE.md
├── DEPLOYMENT.md      # Railway runbook
├── go.mod
└── go.sum
```

---

## Development Conventions

### General
- Go version: 1.22+ (use standard library routing enhancements where appropriate)
- All errors must be handled explicitly — no blank `_` error discards in production paths,
  because a swallowed error resurfaces later as corrupt state or a silent failure
- Use structured logging (log/slog, standard library) — no fmt.Println in handlers, because
  structured fields are filterable in production logs while bare prints bypass levels entirely
- Environment variables for all config (DSN, session secret, OAuth client ID, etc.) — config
  differs per environment and must never be baked into the binary or committed
- Never hardcode credentials or secrets — a hardcoded secret leaks via the repo/image and
  cannot be rotated without a rebuild
- Non-critical writes after a successful external action (e.g. post_history insert
  after a Bluesky post) run in a detached goroutine — never fail the user action

### Naming
- Handlers: `HandleGetTemplates`, `HandleCreateTemplate` etc. (Handle + HTTP verb + resource)
  — a predictable scheme makes a handler's route and method obvious from its name, and greppable
- sqlc query files: one file per domain (`users.sql`, `templates.sql`) — keeps each domain's
  queries and their regenerated code together, so diffs and lookups stay local
- Migration files: `000001_create_users.up.sql` / `000001_create_users.down.sql` — golang-migrate
  discovers and orders migrations by exactly this sequential up/down naming

### Database
- Always use parameterised queries (sqlc enforces this) — prevents SQL injection
- Migrations are sequential and never edited after creation — add a new migration to fix mistakes,
  because an already-applied migration never re-runs, so editing it desyncs migrated environments
- The `did` column is the user identifier in all foreign key relationships, not `handle` —
  handles can change, DIDs cannot (see the OAuth decision above)

### AT Protocol / Bluesky
- Always use the indigo library for post construction — never build `app.bsky.feed.post`
  records manually, because posts carry byte-offset facets that hand-built records corrupt (Gotcha 6)
- Token refresh is reactive — it happens inside `DoWithAuth` during XRPC calls, NOT in
  `ResumeSession`; all callers for a session must share one `ClientSession` instance (see Gotcha 25)
- Detect upstream Bluesky 429s (`bluesky.IsRateLimitError`) and surface a readable 429
  to the client; never a generic 500
- Respect Bluesky rate limits; surface errors clearly rather than silently failing

### HTMX
- Partial templates live in `/templates/partials/` — keeps HTMX fragments separate from
  full-page templates so each is easy to find and reuse
- HTMX responses return only the relevant partial, not a full page — HTMX swaps the response
  into a target node, so a full page would nest document structure inside it
- Keep `hx-` attributes in the HTML — behaviour stays declarative and inspectable in the
  markup; do not drive HTMX behaviour from JavaScript
  (exception: `htmx.ajax()` for programmatic feed swaps in app.js)
- JSON API handlers and HTMX handlers stay separate — never make a JSON endpoint
  return HTML or vice versa, because the JSON API is the iOS contract and must stay HTML-free

### Security
- Session cookies: HMAC-SHA256 signed, HttpOnly, Secure in production, SameSite=Lax,
  constant-time comparison (`hmac.Equal`) — the signature prevents forgery, HttpOnly blocks
  JS theft, SameSite mitigates CSRF, and constant-time compare avoids signature timing leaks
- Security headers middleware on all routes (nosniff, frame deny, referrer policy, CSP
  with 'unsafe-inline' — TODO: migrate to nonces) — defence-in-depth against MIME-sniffing,
  clickjacking, and referrer leakage
- Rate limits: 10/min per DID on posting; 60/min per DID on likes + template CRUD;
  429 + Retry-After on breach — limits are judgment values: tight enough to stop a runaway
  client from damaging DraftSky's standing with Bluesky, loose enough that a human never hits them
- Server-side validation is the enforcement; JS counters are UX only
- Ownership verification on every template mutation (user_id in the query) — stops one user
  reading or mutating another user's templates (IDOR)

---

## Definition of Done

A change is DONE only when all of these hold:

- **Wired end-to-end** — handler + query (sqlc regenerated) + template + route all exist;
  `go build ./...`, `go vet ./...`, and the full test suite pass.
- **Pure logic has table tests** — anything expressible as a pure function is tested
  (precedent: `composerState`, `validatePostRefs`, `mapFeedViewPosts`).
- **Executed once against the running artifact** — server rebuilt, browser hard-refreshed,
  and the CHANGED BEHAVIOUR actually driven. Code-walks pass while browsers fail; status-code
  curls verify handlers, not DOM (Gotchas 10, 16). Not optional.
- **Test data cleaned by captured id** — never by a name/handle or other human-meaningful
  field (Gotcha 19).
- **Docs updated** — CLAUDE.md if a lesson was learned (new Gotcha); DECISIONS.md appended
  if a significant choice was made.
- **Not committed** — Roger commits. Do not `git commit` or `git push`.

---

## Gotchas (hard-won — read before writing code)

Append-only list — check the current highest number before adding.

1. **`#ZgotmplZ` on AT URIs in data attributes.** Go's html/template `attrType()`
   strips the `data-` prefix from custom attributes and treats any remaining name
   containing "uri"/"url"/"src" as a URL context. `at://` is not an allowlisted scheme,
   so the value is replaced with `#ZgotmplZ`. Fix: the `safeAtURI` template function —
   validates against `^at://[a-zA-Z0-9:._/\-]+$` and returns `template.URL`. Only ever
   use it for validated AT URIs, never arbitrary content. (`hx-vals` is immune — it's
   classified contentTypePlain.)
2. **HTMX sends form data, not JSON.** `hx-post` sends
   `application/x-www-form-urlencoded`; `hx-delete` may use query params. Use
   `c.PostForm()` / `c.Query()` in HTMX-triggered handlers — `c.ShouldBindJSON` returns
   400 and HTMX silently discards the response.
3. **sqlc + `unnest()`.** Set-returning functions need an explicit cast
   (`tag::text AS tag`) or sqlc generates `interface{}`.
4. **Trailing newlines are semantic in the composer.** `submitPost()` uses
   `.trimStart()`, never `.trim()` — trailing newlines control where the template
   suffix lands. Server-side, normalise `\r\n` → `\n` before the suffix separator check
   in `Post()`. `updateCounter()` in app.js mirrors the same separator logic.
5. **Escaping order in `highlightHashtags`.** Run the hashtag regex on plain text, then
   escape each segment individually. Escaping first corrupts offsets and double-escapes
   (`'` → `&#39;` rendered literally).
6. **Facet byte offsets are UTF-8 bytes.** `FindAllStringSubmatchIndex` already returns
   byte indices; never convert through rune positions. Emoji will corrupt facets otherwise.
7. **Character counting is graphemes.** Bluesky counts graphemes; use `Intl.Segmenter`
   (with fallback) in JS so DraftSky's counter agrees with the platform.
8. **Railway's variable UI can display values incorrectly** (phantom backticks). Use
   the Raw Editor to verify what's actually stored.
9. **Empty slices, not nil.** JSON list responses initialise as
   `make([]T, len(rows))` / `[]T{}` so clients receive `[]`, never `null`.
10. **Hard-refresh after JS changes.** Browsers cache app.js aggressively; a "fix that
    didn't work" is often a cached file. Cmd+Shift+R before debugging further.
11. **CNAME can't sit on a root domain** (Hover has no ALIAS). Root domain points an
    A record at Railway's IP; the Go middleware handles the www redirect. Hover's
    forward service doesn't do HTTPS and proved unreliable.
12. **Gin route ordering.** Static segments must be registered before parameterised
    ones (`/api/templates/reorder` before `/api/templates/:id`).
13. **`canPlayType()` returns 'maybe' (truthy) for HLS on Chromium** — never use it to
    choose the native path first. Order: hls.js/MSE first, native HLS fallback, error
    panel last resort. hls.js is self-hosted at `/static/vendor/hls.min.js` (third-party
    CDN was a reliability and Brave-Shields liability).
14. **Media elements need `crossOrigin='anonymous'` before any src assignment** —
    no-cors media fetches poison the HTTP cache with ACAO-less entries that CORB-block
    hls.js's CORS XHRs later. The attribute is load-bearing; do not remove it.
15. **hls.js: attach before load.** `loadSource` before `attachMedia` fetches playlists
    but never schedules fragments (no MediaSource to feed) — silent stall at 0:00. Gate
    `loadSource` on the `MEDIA_ATTACHED` event.
16. **Code-walks don't catch lifecycle-ordering errors.** Anything involving an async
    external library (hls.js, HTMX internals) needs one real browser execution before
    it counts as verified. Status-code curls verify handlers, not DOM behaviour.
17. **`hx-on` bodies are dead under our CSP.** HTMX evaluates `hx-on::*` attribute
    bodies with `new Function()`, which the no-'unsafe-eval' CSP blocks. The failure
    is silent: the EvalError prints to console but HTMX carries on — the request fires
    and the swap happens, only the handler body is skipped, so it looks like it works
    until a side effect (error rendering, input clearing) never runs. Use delegated
    `htmx:*` listeners in app.js instead (one `htmx:afterRequest` dispatcher keyed on
    `evt.detail.elt`). Plain-JSON `hx-vals` is fine; `hx-vals="js:..."` would not be.
18. **`putRecord` + indigo's non-omitempty `SwapRecord` = `InvalidSwap`.** indigo's
    `RepoPutRecord_Input.SwapRecord` is `*string` with **no** `omitempty`, so a nil value
    serialises as `"swapRecord": null`. In AT Protocol a *null* swapRecord is not "skip
    the check" — it asserts *the record does not currently exist*. Updating an existing
    record (e.g. `app.bsky.actor.profile/self` to edit a bio) therefore fails with
    `HTTP 400 InvalidSwap: Record was at <cid>` — nothing is written, but the write is
    rejected. Fix: on the get-then-put, capture the current record's CID from the
    `getRecord` response and pass it as `SwapRecord` (a real compare-and-swap, which also
    guards against clobbering avatar/banner blobs in a read→write race). Leave it nil only
    for a genuine create (no record yet), where null="assert none exists" is correct. This
    passed build, unit tests, and template-render tests — it only surfaced against a live
    PDS (see Gotcha 16: async/external-API behaviour needs one real execution).
19. **Test mutations touch only rows the test created.** A test that writes to a shared
    database (templates, post_history, users) must confine every insert/update/delete to
    rows it created in that same run, and destructive cleanup must match on the **id
    captured at creation time**, never on a name/handle/other human-meaningful field.
    Matching cleanup on a name (`DELETE ... WHERE name = 'Devils Game'`) will silently
    wipe a real row a developer happens to have with that name; matching on the captured
    id (`WHERE id = <id returned by the create>`) cannot. Capture the id the create
    returns, use it for teardown, and scope every assertion to it.
20. **A card-level click guard self-blocks when reused on a nested card.** The outer
    `.post-card` navigates via `navigateToThread`, whose blocklist (`evt.target.closest`)
    includes `.quoted-card` and `.post-video` so clicks *inside* a quote/video don't
    navigate the OUTER card. Wiring that SAME function on the nested `.quoted-card`'s own
    onclick made every click inside it match `.quoted-card` (ancestor-or-self) and return
    early — the quoted-card click-through was dead for all quotes. A guard that names the
    handler's own container must not be reused as that container's own handler. Nested
    cards get their own nav fn (`navigateToQuoted`) with no self-referential entry; rely
    on the interactive children's `stopPropagation` to keep them out of it, not on a
    blocklist. (Verified with a byte-identical-function DOM harness — this is a
    synchronous selector bug, so a real click, not a code-walk, is what proves it.)
21. **Moderation viewer flags live on the AUTHOR's viewer state, not the post's.**
    `FeedDefs_ViewerState` (a post's `pv.Viewer`) carries only `like`/`repost`/`bookmarked`/
    `threadMuted`/`pinned` — NOT muted/blocked. The moderation flags (`Muted`, `MutedByList`,
    `BlockedBy`, `Blocking`, `BlockingByList`, and `Following` for exclude-following) are on
    `ActorDefs_ViewerState`, reached via `pv.Author.Viewer`. The reposter carries its own copy
    at `item.Reason.FeedDefs_ReasonRepost.By.Viewer` (`By` is a full ProfileViewBasic), so a
    repost by a muted account can be dropped without an extra fetch. Reaching for
    `post.Viewer.Muted` compiles to nothing useful — it isn't there. Feed-side moderation
    (roadmap item 9) filters at the single `mapFeedViewPosts` chokepoint using the author/
    reposter `ActorDefs_ViewerState`; muted words come from `getPreferences`' `mutedWordsPref`.
22. **searchPosts moderation — VERIFIED no-leak against a live block (2026-07-12).** The open
    worry was that `searchPosts` (the merged hashtag feed) might not server-filter blocks the
    way getTimeline/getFeed do, and might also omit `Author.Viewer`, letting blocked authors
    leak. `GetHashtagFeed` re-applies `authorHidden` + muted-words defensively. Live check with
    a real blocked account (posts #EVZ daily): DraftSky's `#EVZ` hashtag feed contained ZERO of
    their posts — the newest result was 6 months stale despite them posting #EVZ the prior day
    (offseason, ~sole #EVZ poster), i.e. their recent posts were dropped. Blocked accounts do
    NOT leak. Could not isolate server-prefilter vs our `authorHidden` from the client, but the
    outcome is correct and `authorHidden` is unit-tested. Also confirmed live: a blocked account
    as a thread ancestor renders the "Post unavailable (blocked)" gap; `getAuthorFeed` for a
    blocked account errors upstream (server-enforced) and degrades to an empty feed. Muted-WORD
    filtering was likewise verified (a muted tag emptied its feed while a control tag stayed
    full, and exact-tag precision held: muting `bluejays` did not hide `#TorontoBlueJays`).
23. **URLs need link facets or they post as dead text — and mention/link detection can
    collide.** Bluesky renders URLs as links only when the post record carries an
    `app.bsky.richtext.facet#link` facet; without one, a typed URL (`draftsky.social`)
    posts as plain, unclickable text (found live). `buildLinkFacets`/`DetectLinks`
    (`internal/bluesky/links.go`) mirror Bluesky's own composer (`@atproto/api`
    `URL_REGEX` + `isValidDomain`; indigo ships no link helper): scheme URLs
    (`https?://…`) and bare domains with optional path, boundary-anchored (start /
    whitespace / `(`), trailing punctuation + one unmatched `)` stripped. A bare
    domain's facet `uri` gets `https://` prepended while the **text stays as typed**.
    Bare domains are TLD-validated against the full IANA list (`tlds.go`, the `tlds`
    npm list Bluesky uses) so `photo.jpg`/`e.g` are not linkified. Two collisions to
    respect: (a) a bare-domain matcher and the mention matcher both see
    `@rogersherman.com` — mentions/hashtags are detected FIRST and their byte ranges
    excluded from link detection, and the `@`/`#` prefix is not a URL boundary char so
    it never matches anyway (belt + suspenders); (b) the UI's *unanchored* hashtag
    regex would match the `#anchor` inside `https://x.com/#anchor`, so `highlightFacets`
    detects links FIRST and adds hashtags/mentions only when clear of a link range.
    Feed URLs render as real `<a class="post-link" target=_blank
    rel="noopener noreferrer nofollow" onclick=stopPropagation>` with the href built
    from the same validated detection (validate → mark safe, Gotcha 1's spirit), never
    raw text into an attribute. Facets stay byte-sorted in one slice; detection runs on
    combined body+suffix. Live-verified 2026-07-13 (Gotcha 18 class): a posted URL
    rendered clickable both in DraftSky's feed and on bsky.app.
24. **Concurrent `ResumeSession` races the single-use OAuth refresh token.** AT Protocol
    refresh tokens are ROTATED on every refresh (single-use). If N goroutines/requests
    call `oauthApp.ResumeSession` for the same session while the access token is expired,
    they each try to refresh: the first rotates the token and the losers fail with
    `HTTP 400 invalid_grant: refresh token rotated concurrently` (preceded by benign
    `use_dpop_nonce` retries — those are normal DPoP, not the failure). The async home
    shell made this acute and VISIBLE: three regions (`/feed/tabs`, `/feed/following`,
    the immediate unread poll) fire as separate concurrent requests on cold load, so a
    just-expired token spuriously degraded one of them ("feeds unavailable"). The old
    synchronous home had the same race (buildLayoutBase resumed in two concurrent
    goroutines) but hid it — no note, and the later sequential feed fetch used the
    freshly-rotated token. Fix: `feed.Client.resumeAPIClient` serialises per session with
    a `sync.Map[sessionID]*sync.Mutex`, holding the lock ONLY across `ResumeSession` (the
    refresh point) and releasing before the XRPC call — so the first resumer refreshes,
    the rest see a valid token and skip it, and the actual feed fetches still run
    concurrently. Only surfaces against a live PDS with an expired token (Gotcha 16
    class); the mutex map grows unbounded like the rate limiter (same tech-debt bucket).
    The `bluesky.Poster` resume path is not yet serialised (posting isn't self-concurrent).
    **CORRECTION (see Gotcha 25):** this fix did NOT work — `ResumeSession` is not "the
    refresh point" (it performs no refresh), so the mutex never covered the rotation and
    the burn kept happening. Superseded by the ClientSession cache in Gotcha 25.
25. **The real fix for the refresh-token burn: cache the `ClientSession`, don't just lock
    `ResumeSession`.** The 2026-07-17 token-burn incident (a user's refresh token died with
    `invalid_grant`; every feed region then showed the "Bluesky isn't responding" network
    notice — a lie, since only re-login could recover it) exposed that Gotcha 24's mutex was
    load-bearing on a false premise. Tracing the vendored indigo (`atproto/auth/oauth`):
    (a) `ClientApp.ResumeSession` does **no** token refresh — it loads session data from the
    store and builds a *fresh* `ClientSession` each call. (b) The refresh is **reactive**,
    happening inside `ClientSession.DoWithAuth` on a 401 *during the XRPC call* — which runs
    OUTSIDE `resumeAPIClient`'s mutex. indigo does not persist access-token expiry
    (`session.go` TODO), so proactive refresh-under-lock is impossible. (c) The `use_dpop_nonce`
    dance on the token endpoint is already handled correctly by indigo's `postToAuthServer`
    (`for range 2` loop captures the `DPoP-Nonce` header and retries) — a nonce renegotiation
    does NOT burn the token (the server rejects the 400 *before* consuming the grant), so the
    nonce retry was never the bug. The bug is `session.go`'s own warning: *"concurrent calls to
    distinct ClientSession instances for the same session could result in clobbered session
    data."* Building a fresh instance per call meant the async shell's ~3 concurrent regions
    each refreshed the same stale refresh token R0 → first wins (R0→R1), losers send the
    consumed R0 → `invalid_grant` → burned session. PDS degradation widens the window (slow
    requests keep all three holding R0). **Fix:** `feed.Client` caches ONE `*oauth.ClientSession`
    per sessionID (`sync.Map` + `sync.Once`); a single instance is concurrency-safe and
    serialises its own refresh via its internal `sess.lk`, so the rotated token is never sent
    twice (a redundant second refresh may happen but rotates a *valid* token — no burn). The two
    fixes reinforce: eliminating the self-inflicted burn means a surviving `invalid_grant` now
    genuinely signals a dead session, so `auth.IsDeadSession` (string-matches `invalid_grant` /
    `oauth session not found`; indigo exports no typed error — atclient returns the DoWithAuth
    error verbatim, so the string survives) can safely drive re-login UX and `InvalidateSession`
    (evict cache + delete the session row) without risking logging out a hiccuped user. The
    session cookie outlives the OAuth tokens (independent lifetimes), so `RequireSession`/
    `RequireAuth` — which only validate the cookie — pass for a dead session; the deadness only
    surfaces at the feed-fetch layer, which is where it's now classified. The `bluesky.Poster`
    path still builds its own ClientSession (unchanged — posting isn't self-concurrent, but a
    concurrent post + feed refresh could still theoretically race; same tech-debt bucket).
    Live-verified 2026-07-18 by corrupting the stored refresh token (Gotcha 16 class).

---

## Environment Variables

```
DATABASE_URL        PostgreSQL DSN (Railway-injected in production)
SESSION_SECRET      Random 32-byte secret for session signing (openssl rand -hex 32)
OAUTH_CLIENT_ID     https://www.draftsky.social/client-metadata.json (production)
OAUTH_REDIRECT_URL  https://www.draftsky.social/auth/callback (production)
APP_ENV             development | production
PORT                HTTP listen port (default 8080)
ADMIN_DID           Optional. Owner DID that unlocks GET /admin/stats; unset = 404 for all
```

Local dev: no OAUTH_CLIENT_ID → localhost OAuth config; use `http://127.0.0.1:8080`
(not `localhost` — RFC 8252 requires a loopback IP in the redirect URI). Local DB runs
in Docker (`docker start draftsky-dev-db`).

---

## Status & Roadmap

### Shipped (live at www.draftsky.social, private beta)
- Dead-session honesty + refresh-token burn fix — from the 2026-07-17 incident. TWO parts.
  (1) Root cause: Gotcha 24's mutex never covered the token refresh (refresh is reactive in
  indigo's `DoWithAuth`, not in `ResumeSession`), so concurrent home-shell regions each
  refreshed the same single-use refresh token and burned the session with `invalid_grant`.
  Real fix: `feed.Client` now caches ONE `*oauth.ClientSession` per sessionID (`sync.Map` +
  `sync.Once`) — a single instance is concurrency-safe and serialises its own refresh, so the
  rotated token is never re-sent. indigo already handles the `use_dpop_nonce` token-endpoint
  retry, so no client nonce-retry was needed. (2) When a session IS genuinely dead,
  `auth.IsDeadSession` (classifies `invalid_grant`/`oauth session not found` as dead vs
  timeout/5xx as transient) drives honest UX instead of the "Bluesky isn't responding" lie:
  the lazy feed region shows a distinct "Your session has expired — sign in again" notice
  linking to `/login` (the login page's handle-entry form — NOT `/auth/login`, which needs a
  `?handle=` param and 400s bare; caught live); JSON API endpoints return 401
  `{code:"session_expired"}` (the
  notification poll already stops on 401); synchronous full pages (`/thread`, `/notifications`,
  `/profile`) redirect to `/login`; and `InvalidateSession` evicts the cached session + deletes
  the dead OAuth row (only on definitive `invalid_grant`, never on a transient hiccup). See
  Gotchas 24 (corrected), 25. Live-verified 2026-07-18 (corrupted stored refresh token).
- Async home shell (PDS-degradation resilience) — `GET /` renders INSTANTLY with zero
  upstream calls: `HandleHome` uses a pure `buildShellLayout` (no feed client, so
  zero-upstream is structural, not runtime), and the tab bar, centre feed, and
  recent-tags rail are each an HTMX lazy region (`hx-trigger="load"` → `/feed/tabs`,
  `/feed/following`, `/feed/recent-tags`) that shows a CSS skeleton
  (prefers-reduced-motion aware) then its content or a styled "Bluesky isn't responding"
  notice with a working Retry button. Page-blocking feed/chrome fetches fail fast at 6s;
  the tab bar degrades to Following-only + a note. `createRecord` retries once after 2s
  on transient errors (never on 4xx — double-post guard via `atclient.APIError`
  StatusCode) with a 10s per-attempt budget; the composer maps a 502 to "your draft is
  safe" and preserves state; the unread poll fires immediately on load. Fixes the
  2026-07-15 incident (17-20s home render during PDS TLS timeouts). Also serialised the
  per-session OAuth token refresh (Gotcha 24). Live-verified 2026-07-16 (instant shell +
  skeletons + fault-injected notices + Retry re-fetch + restore). See Gotchas 16, 24.
- AT Protocol OAuth login, PostgreSQL-backed OAuth store
- Template CRUD with validation (100/250 rune limits) + live character counters
- Posting with template suffix, correct facets, newline-aware suffix placement
- Following feed + merged hashtag feed + recent-tags rail + infinite scroll
- Likes (with viewer state), image embeds, external link cards, avatars
- Replies with correct AT Protocol threading (root/parent), reply preview in composer
- Reposts with "Reposted by X" attribution and optimistic counts
- Reply labels ("Replying to @handle") and full thread view (/thread, navigate-not-nest)
- Saved feed tabs (pinned feeds from Bluesky preferences, Bluesky-style tab bar;
  unresolvable feeds skipped with two-level WARN observability)
- Quote post rendering (embed.record + recordWithMedia), compact quoted cards
- Feed generator dedup (URI + reposter key) with unit tests
- Inline video playback (hls.js self-hosted, MSE-first, full teardown, error panel)
- CSRF protection (HMAC per-session tokens, header + form fallback, middleware tests)
- Notifications: /notifications view, per-reason click-through, left-rail badge with
  60s polling (paused when tab hidden), JSON endpoints for iOS
- Settings page + theme selector (server-side paid check, instant repaint, free-user
  fallback verified live)
- hx-on → delegated listeners (CSP regression fix: 409 errors + inline-edit errors
  were silently invisible in production)
- @-mentions: outgoing mention facets (concurrent handle resolution, suffix mentions,
  emails excluded), incoming mentions accent-rendered in feeds
- Hashtag context menu — Bluesky-style popover on hashtag click: "See #tag posts" +
  "See #tag posts by @author" (searchPosts author+tag filter, `?author=` on
  /feed/hashtags; validated handle/DID → 400). Reuses the repost-menu pattern; bios
  show one option (no post author); right-rail tags keep direct-switch (your own tags)
- Usage instrumentation + admin stats — `last_seen_at` (migration 000008) stamped by a
  detached-goroutine `TouchLastSeen` middleware with an in-SQL once-per-hour staleness
  gate (no per-request SELECT); owner-only `GET /admin/stats` (RequireAdmin 404s every
  non-owner/no-session case to hide the route) rendering DAU/WAU/MAU + user/content
  totals from two single-pass aggregate queries
- Bluesky mutes + blocks inheritance (contained) — feed content the user muted/blocked on
  Bluesky no longer shows in DraftSky. Account-level drops (muted incl. lists, blockedBy,
  blocking) + muted-word/tag filtering (mutedWordsPref) at the `mapFeedViewPosts` chokepoint,
  the hashtag-feed merge loop, and thread view (muted/blocked ancestors/replies → labelled
  "unavailable" gaps). One getPreferences per render serves both saved feeds and muted words.
  Pure-function filtering with table tests (author flags, muted-word matching incl. whole-word
  `cat`≠`catalogue`, expiry, exclude-following) + a thread-gap render smoke test. Live-verified
  2026-07-12 against a real muted tag and a real blocked account (hashtag feed no-leak + thread
  gap + getAuthorFeed upstream-error). See Gotchas 21–22
- Static asset cache-busting — content-hash `?v=<sha8>` on app.js/style.css (+ hls.min.js
  via a `<meta>` tag app.js reads), computed once at startup from file bytes; `/static`
  now served `Cache-Control: public, max-age=31536000, immutable`. Ends the Gotcha 10
  (stale app.js) class of bug for end users: a deploy that changes an asset mints a fresh
  URL the browser must re-fetch, unchanged assets stay fully cached
- URL link facets — outgoing posts now carry `app.bsky.richtext.facet#link` facets so
  typed URLs render as real links on Bluesky (fixing dead-text URLs), and feed URLs
  render as clickable `<a>` links in DraftSky. Scheme URLs + bare domains (TLD-validated
  against the full IANA list), boundary-anchored, trailing-punctuation stripped, bare
  domains get `https://` prepended (text unchanged); mention/hashtag ranges excluded so
  `@handle.com` stays a mention. `DetectLinks` shared by the facet builder and feed
  rendering; table tests + live-verified 2026-07-13. See Gotcha 23
- Three-column responsive layout, Deep Ocean theme (+3 paid themes defined in CSS)
- Security headers, robots.txt, favicon, tiered rate limiting
- Railway deployment, custom domain, bare-domain 301 redirect
- Free tier enforcement — server-side 5-template cap on POST /api/templates (free plan; 403
  with an upgrade message at the cap; documented count-then-insert race, worst case one bonus
  template — see DECISIONS.md), `RequirePaidPlan` middleware for future paid-only endpoints,
  and trusted-proxy scoping (RFC1918 + CGNAT)

### Current priority order (pre-GA ladder — GA after item 9)
1. **Free tier enforcement** — ✅ **DONE** (see Shipped). Server-side 5-template cap on POST
   /api/templates (403 with a clear message at the cap; documented count-then-insert race,
   worst case one bonus template), `RequirePaidPlan` middleware for future paid-only endpoints,
   and trusted-proxy scoping. Only the conditional CIDR tighten remains (see Technical debt).
2. **Quote posts** — the repost button becomes a small Repost/Quote menu; Quote opens
   the composer in quote mode (like reply mode, carrying `{uri, cid}`), which attaches
   an `app.bsky.embed.record` to the post record. Templates work in quotes. Quote mode
   and reply mode are mutually exclusive in v1.
3. **Profiles** — `/profile/<handle>` view: getProfile header (avatar, banner, display
   name, bio, follower/following/post counts) + getAuthorFeed with cursor pagination
   through the existing feed rendering. Own profile gets text-only editing (display
   name + bio via putRecord on the profile record). Avatar/banner UPLOAD is deferred
   to photo posting post-GA (needs uploadBlob). Clicking avatars/handles anywhere in
   feeds navigates to the profile.
4. **Clickable mentions** — the accent mention spans in feeds get click-through to
   `/profile/<handle>` (stopPropagation so card click-through survives, same pattern
   as hashtags)
5. **Mention typeahead** — composer: typing `@` + chars triggers a debounced
   searchActorsTypeahead dropdown; keyboard navigation (up/down/enter/escape),
   insert-on-select. Works in both new-post and reply/quote modes.
6. **Hashtag context menu** — ✅ **DONE** (see Shipped). Bluesky-style popover on
   hashtag click: "See #tag posts" (existing hashtag feed) and "See #tag posts by
   @author". The searchPosts author+tag question resolved YES — our vendored indigo's
   `FeedSearchPosts` has an `author` param (handle or DID, resolved server-side) that
   combines with `tag`, so the by-author half shipped (no deferral). "Mute #tag"
   remains DEFERRED post-GA (needs a mutes table + filtering on every feed path).
7. **Quoted-card click-through diagnosis + fix** — ✅ **DONE**. Root cause was NOT
   composition-specific and NOT stale cache: the quoted card reused the outer card's
   `navigateToThread`, whose blocklist contains `.quoted-card` / `.post-video` (there
   to stop clicks *inside* a quote from navigating the OUTER card). Reused on the
   quote's own handler, every click self-matched `.quoted-card` (and quoted videos
   matched `.post-video`) → the guard returned early → click-through was dead for ALL
   quotes since `f7634e8` (born broken: that commit added both the nav onclick and the
   `.quoted-card` blocklist entry). Fix: dedicated `navigateToQuoted` (no self-block;
   only a defensive `a, button` guard — the quote's interactive children already
   stopPropagation), wired on `.quoted-card`. Quoted video thumb now navigates to the
   quoted thread ("play it there"). Verified failure + fixed click-map (quoted text /
   quoted video / quoted images / quoted link / outer body / outer link → correct
   destinations) with a byte-identical-function DOM harness. See Gotcha 18.
8. **Usage instrumentation + admin stats** — ✅ **DONE** (see Shipped). Migration
   000008 added `last_seen_at`; the `TouchLastSeen` middleware (mounted on the web +
   api groups after auth) fires a detached-goroutine `TouchUserLastSeen` whose
   once-per-hour staleness gate lives IN the SQL (`WHERE ... last_seen_at IS NULL OR
   < now() - interval '1 hour'`), so no per-request SELECT and no write-per-request.
   `GET /admin/stats` (RequireAdmin, self-validating → 404 for any non-owner/no-session
   so the route stays hidden) renders the layout with two single-pass aggregate queries
   (GetAdminStats FILTER windows + GetContentStats). Email push remains a post-GA upgrade.
9. **Inherit Bluesky mutes + blocks (contained version)** — ✅ **DONE** (see Shipped).
   Filtering converges on `mapFeedViewPosts` (following/custom/author feeds) plus the
   `GetHashtagFeed` merge loop and `GetThread`. (a) `authorHidden` drops posts whose
   author — or, for a repost, the reposter (its own viewer state on `Reason.By`) — is
   muted (incl. mute lists) or block-related, reading `pv.Author.Viewer`
   (`ActorDefs_ViewerState`, NOT the post's viewer state — see Gotcha 21). (b) Muted
   words/tags come from `getPreferences`' `mutedWordsPref` via a single per-render call
   (`GetPreferences` returns saved feeds + muted words together; muted-words-only
   consumers use `GetMutedWords`, no getFeedGenerators round-trip); `matchesMutedWords`
   does whole-word content matching (`cat` ≠ `catalogue`), phrase substring, tag matching,
   expiry, and `exclude-following` via `Author.Viewer.Following`. (c) searchPosts is
   re-filtered client-side because its server-side block filtering is unverified — see
   Gotcha 22 (needs one live run against a blocked account). Thread ancestors/replies that
   are muted/blocked render as labelled "unavailable" gaps (`ThreadEntry`) rather than
   vanishing. Documented simplifications: no diacritic-stripping/URL-special-casing in
   content matching; tag matching uses inline hashtags (not the record's non-inline `tags`
   field); a muted account's own profile feed filters to empty (the post-GA interstitial
   addresses that). FULL parity (collapse-with-"show anyway" UI, muted-thread handling,
   profile-page interstitials, mute expiry display) is post-GA polish — filed below.

After item 9: **GA**.

### Post-GA / longer term
- **Multi-template posting** — select multiple templates per post. Chips UI
  (selected templates as removable chips below the selector, reorderable by
  selection order). Suffixes concatenate in SELECTION order, single space
  between suffixes; the newline-awareness rule (Gotcha 4) applies only between
  body and the first suffix. Tag-aware dedup across combined suffixes (first
  occurrence wins — #NHL appears once even if two templates carry it), which
  makes combination tag-aware rather than string concat. composerState gains a
  suffixes[] input; counter counts the combined deduped result. No free-tier
  interaction — the 5-template cap is on creation, not use (confirmed intent).
- **Hashtag activity counter** (small) — when the post-submit hashtag feed appears,
  show per-tag stats from the already-fetched merged feed: post count and rough
  recency per tag (e.g. "#NJDevils: 47 posts in the last hour"). Answers "which of my
  tags have a live audience right now?" — a counting pass over data already in hand
  plus a small UI strip above the feed.
- **Hashtag performance analytics** (paid-tier flagship) — correlate the user's own
  posts' engagement with the hashtags used over time: "posts with #motosky average
  4.2 likes; #bmwgs averages 0.8." Requires periodic re-fetching of the user's posts'
  like/reply/repost counts (they change for days), engagement snapshots stored against
  the hashtags already in `post_history`, and honest handling of confounders (one viral
  post inflates its tags). This consciously reverses the "no analytics" Out of Scope
  entry for OWN-posts-only analytics — no tracking of other users. This is the anchor
  of the paid tier's value story for channel-runners and self-promoters.
- Browser Notifications API on top of the unread poll (opt-in from settings; OS-level
  notification when count rises while tab unfocused)
- **Full mute/block parity** — collapse-with-"show anyway" UI instead of dropping,
  muted-thread handling, profile-page interstitials for blocked/blocking accounts,
  muted-word expiry times, and "Mute #tag" write-path (creates the preference on the
  user's Bluesky account so it inherits everywhere — supersedes the old local-mutes
  idea). Builds on pre-GA item 9.
- **Feed position persistence** — Home (and app open) currently always lands on
  Following; it should restore the last-active feed tab. Scroll position restores ONLY
  for in-app navigation (Home from Notifications → last feed at previous scroll);
  reload or fresh arrival → last feed at TOP. Shape: last tab in localStorage
  (survives sessions), scroll in sessionStorage keyed per-feed (dies with the tab —
  gives reload-to-top nearly free, but distinguish in-app HTMX nav from full reload
  carefully). Fall back to Following when the remembered feed no longer exists
  (unpinned — the saved-feed-skip handles absence).
- **GIF embeds render as link cards, not inline media** — Bluesky ships Tenor/Klipy
  GIFs as `embed.external` with a media URL and its client special-cases them into an
  inline looping player; DraftSky shows the generic link card. Detect GIF-hosting
  external embeds (tenor.com / klipy.com media URLs) and render an inline looping
  <video muted loop playsinline> or <img> instead.
- **Video in quoted cards is thumbnail-only with no path to playback** — deliberate
  quote-session scope, but real posts (quote + video) show the cost. Likely fix:
  clicking the quoted video thumbnail navigates to the quoted post's thread where it
  plays. Revisit alongside the GIF work.
- Avatar/banner editing on profiles (rides with photo posting — uploadBlob)
- "Mute #tag" in the hashtag context menu (mutes table + filtering on every feed path)
- Search (searchPosts, searchActors)
- Bookmarks (local until AT Protocol supports natively)
- Photo posting (uploadBlob)
- Direct messages (chat.bsky.convo.* — complex, separate proxy infrastructure)
- Tabbed (per-hashtag) feed view as alternative to merged
- iOS app (SwiftUI, same /api/* endpoints, feature parity)
- Template sharing / starter packs

### Technical debt
- CSP uses 'unsafe-inline' — migrate to nonces
- Rate limiter sync.Map grows unbounded — needs cleanup or Redis at scale
- Trusted proxies: RFC1918+CGNAT scoping shipped; tighten to Railway's specific CIDR only if Railway ever documents one.

---

## UI Design

### Layout
Three-column layout matching the Bluesky/Twitter convention:

```
┌──────────────┬─────────────────────┬──────────────┐
│   Left Rail  │     Centre Feed     │  Right Rail  │
│              │                     │              │
│  Avatar      │  [feed controls]    │ Recent Tags  │
│  Handle      │  [post cards]       │ #NJDevils    │
│  ─────────   │                     │ #NHL         │
│  Home        │                     │ #motosky     │
│  Templates   │                     │ ...          │
│  Settings    │                     │              │
│  ─────────   │                     │              │
│  [New Post]  │                     │              │
│  Sign out    │                     │              │
└──────────────┴─────────────────────┴──────────────┘
```

- **Left rail:** avatar, handle, nav links, divider, New Post button, Sign out
- **Centre feed:** Following feed on load; feed controls partial shows active feed
  (Following vs "Hashtag Feed: #x #y") with Back to Following when applicable
- **Right rail:** last 10 unique hashtags the user has posted (from `post_history`),
  most recent first; clicking one switches the centre feed
- **Responsive:** max-width 1280px centred; right rail hides below 600px; left rail
  collapses to icon strip below 480px

### Composer (modal)
- Avatar, textarea ("What's up?" / "Write your reply" in reply mode)
- Reply mode: compact preview (author + ~100 chars) of the post being replied to
- Template selector dropdown with suffix preview below the textarea
- Grapheme-accurate character counter (300 limit, counts body + suffix + separator)
- Post button (disabled when over limit or empty), Cancel, Escape/overlay to close

### Colour Palette — Deep Ocean (default)

| Role         | Hex       | Usage                                      |
|--------------|-----------|--------------------------------------------|
| Background   | `#070d1a` | Page background                            |
| Surface      | `#0d1829` | Left rail, right rail, card backgrounds    |
| Card         | `#132038` | Post cards, modal background               |
| Accent       | `#34d399` | Hashtags, active nav, buttons, links       |
| Text         | `#dbeafe` | Primary text                               |
| Muted        | `#4b6080` | Secondary text, timestamps, labels         |

CSS variables must be used throughout — never hardcode hex values in component styles.
All colours are defined in the `:root {}` block in `/static/style.css`.

### Themes

Themes are sets of CSS variable overrides. The user's theme is injected as a class on
the `<body>` tag by the Go template, based on the `theme` column. Free users are locked
to `ocean`; paid users can select any theme (settings page — pending).

| Theme key  | Name              | Plan     | Accent    | Base                    |
|------------|-------------------|----------|-----------|-------------------------|
| `ocean`    | Deep Ocean        | Free     | `#34d399` | `#070d1a` / `#0d1829`  |
| `slate`    | Midnight Slate    | Paid     | `#7c9ef8` | `#0f1117` / `#181c27`  |
| `amber`    | Charcoal & Amber  | Paid     | `#f59e0b` | `#111111` / `#1a1a1a`  |
| `graphite` | Graphite & Sky    | Paid     | `#38bdf8` | `#131416` / `#1c1f23`  |

**Implementation rules:**
- `:root` holds the `ocean` defaults; `body.slate {}`, `body.amber {}`,
  `body.graphite {}` override only what differs
- A free user whose `theme` column is somehow a paid theme falls back to `ocean` —
  enforced in the template data layer (`buildLayoutBase`), not in CSS
- Theme keys from clients are validated against the allowlist server-side
- Adding a theme = one CSS block + one row in this table

### Typography
System font stack — no external font dependencies.

---

## Monetisation

DraftSky uses a freemium model on web and an ad-supported + one-time purchase model on iOS.

**Web:**
- Free tier: up to 5 templates, Following feed, posting, replies, Ocean theme only
- Paid tier: unlimited templates, all themes, tabbed hashtag feed (future), hashtag
  performance analytics (future — the paid flagship; see roadmap)
- Enforced via the `plan` column (free-tier 5-template cap shipped — see Shipped)

**iOS (future):**
- IAP-only leaning; ads under doubt (ATT prompt, SDK weight, review complexity) - final call at iOS build time, see DECISIONS.md.
- Purchase tied to the user's DID, not the device — buying on iOS sets `plan = 'paid'`
  server-side, upgrading web too
- **Server-side Apple receipt verification is mandatory** — never trust the client
- Apple Small Business Program (15% vs 30%) — apply at launch

---

## Out of Scope (for now)

- Multi-platform support (Mastodon, Threads etc.) — Bluesky only
- Scheduling posts
- Analytics or engagement tracking of OTHER users (own-posts hashtag performance
  analytics is planned — see roadmap; this line covers tracking anyone else)
- Team/shared template libraries
- Photo posting (requires blob upload — on the long-term roadmap)

---

## Key Dependencies

```
github.com/gin-gonic/gin
github.com/jackc/pgx/v5
github.com/bluesky-social/indigo
github.com/golang-migrate/migrate/v4
golang.org/x/time/rate
sqlc (CLI tool — see https://sqlc.dev)
```
