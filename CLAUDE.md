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
Hashtags in Bluesky are not plain text — they are `facets` in the `app.bsky.feed.post`
lexicon (rich text byte-range annotations). Always construct posts using the indigo
library and the existing helpers in `internal/bluesky/bluesky.go` (`buildHashtagFacets`,
`ExtractHashtags`), never by naive string concatenation. Byte offsets are UTF-8 byte
positions, not rune positions.

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
000007_create_oauth_store.

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
| GET    | /                | Three-column layout, Following feed on load     |
| GET    | /feed/following  | HTMX partial — Following feed                   |
| GET    | /feed/hashtags   | HTMX partial — merged hashtag feed              |
| GET    | /templates       | Template management page                        |
| GET    | /settings        | Settings (page not yet built — priority item)   |
| GET    | /login           | Login page (public; bounces if authed)          |

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
- All errors must be handled explicitly — no blank `_` error discards in production paths
- Use structured logging (log/slog, standard library) — no fmt.Println in handlers
- Environment variables for all config (DSN, session secret, OAuth client ID, etc.)
- Never hardcode credentials or secrets
- Non-critical writes after a successful external action (e.g. post_history insert
  after a Bluesky post) run in a detached goroutine — never fail the user action

### Naming
- Handlers: `HandleGetTemplates`, `HandleCreateTemplate` etc. (Handle + HTTP verb + resource)
- sqlc query files: one file per domain (`users.sql`, `templates.sql`)
- Migration files: `000001_create_users.up.sql` / `000001_create_users.down.sql`

### Database
- Always use parameterised queries (sqlc enforces this)
- Migrations are sequential and never edited after creation — add a new migration to fix mistakes
- The `did` column is the user identifier in all foreign key relationships, not `handle`

### AT Protocol / Bluesky
- Always use the indigo library for post construction — never build `app.bsky.feed.post`
  records manually
- Token refresh is transparent via `ResumeSession` (handles DPoP + refresh automatically)
- Detect upstream Bluesky 429s (`bluesky.IsRateLimitError`) and surface a readable 429
  to the client; never a generic 500
- Respect Bluesky rate limits; surface errors clearly rather than silently failing

### HTMX
- Partial templates live in `/templates/partials/`
- HTMX responses return only the relevant partial, not a full page
- Keep `hx-` attributes in the HTML; do not drive HTMX behaviour from JavaScript
  (exception: `htmx.ajax()` for programmatic feed swaps in app.js)
- JSON API handlers and HTMX handlers stay separate — never make a JSON endpoint
  return HTML or vice versa

### Security
- Session cookies: HMAC-SHA256 signed, HttpOnly, Secure in production, SameSite=Lax,
  constant-time comparison (`hmac.Equal`)
- Security headers middleware on all routes (nosniff, frame deny, referrer policy, CSP
  with 'unsafe-inline' — TODO: migrate to nonces)
- Rate limits: 10/min per DID on posting; 60/min per DID on likes + template CRUD;
  429 + Retry-After on breach
- Server-side validation is the enforcement; JS counters are UX only
- Ownership verification on every template mutation (user_id in the query)

---

## Gotchas (hard-won — read before writing code)

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

---

## Environment Variables

```
DATABASE_URL        PostgreSQL DSN (Railway-injected in production)
SESSION_SECRET      Random 32-byte secret for session signing (openssl rand -hex 32)
OAUTH_CLIENT_ID     https://www.draftsky.social/client-metadata.json (production)
OAUTH_REDIRECT_URL  https://www.draftsky.social/auth/callback (production)
APP_ENV             development | production
PORT                HTTP listen port (default 8080)
```

Local dev: no OAUTH_CLIENT_ID → localhost OAuth config; use `http://127.0.0.1:8080`
(not `localhost` — RFC 8252 requires a loopback IP in the redirect URI). Local DB runs
in Docker (`docker start draftsky-dev-db`).

---

## Status & Roadmap

### Shipped (live at www.draftsky.social, private beta)
- AT Protocol OAuth login, PostgreSQL-backed OAuth store
- Template CRUD with validation (100/250 rune limits) + live character counters
- Posting with template suffix, correct facets, newline-aware suffix placement
- Following feed + merged hashtag feed + recent-tags rail + infinite scroll
- Likes (with viewer state), image embeds, external link cards, avatars
- Replies with correct AT Protocol threading (root/parent), reply preview in composer
- Three-column responsive layout, Deep Ocean theme (+3 paid themes defined in CSS)
- Security headers, robots.txt, favicon, tiered rate limiting
- Railway deployment, custom domain, bare-domain 301 redirect

### Current priority order
1. **Reposts** — `app.bsky.feed.repost` toggle alongside likes, plus "Reposted by X"
   attribution on feed cards (from the timeline item's `reason` field,
   `app.bsky.feed.defs#reasonRepost` — same feed-mapping code, same session)
2. **Reply context + thread view** — "Replying to @handle" label on reply cards (from
   the post's `reply` context); clicking a post opens the full thread view via
   `app.bsky.feed.getPostThread` (root → intermediate replies → the clicked post and
   its replies), Bluesky-style. New route/view; also delivers "click any post to see
   its replies" for free.
3. **Saved feeds (read-only)** — fetch the user's pinned/saved feeds; tabs across the
   top of the centre column (Bluesky-style) to switch between them, alongside
   Following and hashtag feeds
4. **CSRF protection** — tokens on all state-mutating endpoints
5. **Settings page + theme selector** — page scaffold; theme switching for paid users
   (server-side plan check; validate theme key against allowlist)
6. **Free tier enforcement** — 5-template limit, `RequirePaidPlan` middleware; scope
   trusted proxies to Railway's CIDR

### Post-GA / longer term
- **Hashtag context menu** — Bluesky-style popover on hashtag click/long-press:
  "See #tag posts" (existing hashtag feed), "See #tag posts by user" (needs author
  feeds), "Mute #tag" (needs a mutes table + feed filtering). Deferred until
  profiles/author feeds exist; plain click switches to the hashtag feed until then.
- Notifications (listNotifications) + unread badge
- Own profile view/edit; other user profiles (getProfile, getAuthorFeed)
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
- Trusted proxies at 0.0.0.0/0 pending Railway CIDR scoping

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
- Paid tier: unlimited templates, all themes, tabbed hashtag feed (future)
- Enforced via the `plan` column (enforcement pending — priority item 5)

**iOS (future):**
- Ads by default (AdMob or equivalent); non-consumable IAP via StoreKit 2 removes them
- Purchase tied to the user's DID, not the device — buying on iOS sets `plan = 'paid'`
  server-side, upgrading web too
- **Server-side Apple receipt verification is mandatory** — never trust the client
- Apple Small Business Program (15% vs 30%) — apply at launch

---

## Out of Scope (for now)

- Multi-platform support (Mastodon, Threads etc.) — Bluesky only
- Scheduling posts
- Analytics or engagement tracking
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
