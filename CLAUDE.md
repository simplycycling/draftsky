# DraftSky — CLAUDE.md

## Project Overview

DraftSky is a multi-user Bluesky posting client with template support and an integrated
feed. Users authenticate via Bluesky OAuth, create named templates (pre-composed hashtag
sets and recurring text), and select a template when composing a post. The template's
suffix is appended to their post before it is submitted to Bluesky via the AT Protocol.

The default view is the user's Following feed. After a post is submitted, DraftSky
automatically switches to a merged hashtag feed — a combined, recency-sorted stream of
all hashtags used in that post — so the user can immediately see the conversation they
have just entered. The Following feed is always accessible to return to.

The primary motivation is reducing repetitive hashtag entry for topic-specific posting
(e.g. sports coverage, hobby communities, professional topics). DraftSky is intended as
a public, production web application with a companion iOS app in a later phase.

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
| iOS (Phase 3)| SwiftUI, hitting the same JSON API              |

---

## Architecture

DraftSky is an API-first application. The Go backend serves both the HTMX-driven web UI
and a JSON API. The iOS app (Phase 3) will consume the same JSON API endpoints.

```
┌─────────────────────────────────────────┐
│            Go (Gin) Backend             │
│                                         │
│  /auth/*         AT Protocol OAuth      │
│  /api/templates  Template CRUD (JSON)   │
│  /api/post       Compose + post (JSON)  │
│  /api/feed       Following + hashtag    │
│  /*              HTMX web UI            │
└───────────────┬─────────────────────────┘
                │ same /api/* endpoints
      ┌─────────┴──────────┐
      │                    │
  HTMX Web UI          iOS App
  (Go templates)       (SwiftUI, Phase 3)
```

The web UI is server-side rendered using Go's `html/template` package. HTMX handles
dynamic interactions (template CRUD without full page reloads, live post preview,
character count, feed polling). There is no separate frontend build step and no
JavaScript framework.

---

## Key Architectural Decisions

### PostgreSQL over SQLite
This is a public multi-user application. PostgreSQL is the production database from day
one. Do not suggest SQLite, even for development — use a local PostgreSQL instance or
a Railway dev environment to keep parity.

### sqlc for database access
All database queries are written as raw SQL in `/db/queries/`. sqlc generates type-safe
Go code from these queries. Do not use an ORM. Do not write manual `database/sql` query
boilerplate. When adding a new query, add it to the appropriate `.sql` file and re-run
`sqlc generate`.

### AT Protocol OAuth (not app passwords)
DraftSky is multi-user and public. Auth uses the AT Protocol OAuth 2.0 PKCE flow, not
app passwords. Each user authenticates through their own PDS (Personal Data Server).
The user's DID (Decentralised Identifier) is the canonical user identifier in the
database — not their handle, which can change.

### HTMX over a JS framework
The web UI is intentionally kept in Go-land. HTMX attributes drive dynamic behaviour.
Do not introduce React, Vue, or any npm-based frontend toolchain. Vanilla JS is
acceptable for small enhancements (e.g. character counter) but should be minimal and
inline or in a single `/static/app.js` file.

### Bluesky posts use facets
Hashtags in Bluesky are not plain text — they are `facets` in the `app.bsky.feed.post`
lexicon (rich text byte-range annotations). Always construct posts using the indigo
library's richtext helpers, never by naive string concatenation. This applies to both
hashtags and mentions.

### Feed behaviour
The default feed is the user's Following feed (`app.bsky.feed.getTimeline`). After a
post is successfully submitted, the UI automatically switches to a merged hashtag feed
built by querying `app.bsky.feed.searchPosts` for each hashtag in the post, merging
the results, and sorting by `indexedAt` descending. The merge happens server-side — the
`/api/feed/hashtags` endpoint accepts a list of hashtags and returns a single unified
feed. The user can return to their Following feed at any time.

Phase 2 will introduce a toggle between the merged view and a per-hashtag tabbed view.
Until then, merged is the only mode and the toggle does not exist in the UI.

---

## Database Schema

```sql
CREATE TABLE users (
    id            SERIAL PRIMARY KEY,
    did           TEXT UNIQUE NOT NULL,   -- e.g. did:plc:abc123 (canonical identifier)
    handle        TEXT,                   -- e.g. roger.bsky.social (may change)
    access_token  TEXT,
    refresh_token TEXT,
    token_expiry  TIMESTAMPTZ,
    plan          TEXT NOT NULL DEFAULT 'free', -- 'free' | 'paid'; set to 'paid' on verified IAP
    theme         TEXT NOT NULL DEFAULT 'ocean', -- see Themes section; paid users only for non-default
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
    name       TEXT NOT NULL,             -- e.g. "Devils Game"
    suffix     TEXT NOT NULL,             -- e.g. "#NJDevils #GoAvsGo #NHL"
    position   INTEGER DEFAULT 0,         -- display order in dropdown
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, name)
);
```

---

## API Endpoints

### Auth
| Method | Path                  | Description                          |
|--------|-----------------------|--------------------------------------|
| GET    | /auth/login           | Initiate AT Protocol OAuth PKCE flow |
| GET    | /auth/callback        | OAuth callback, exchange code        |
| POST   | /auth/logout          | Clear session                        |

### Templates (JSON API)
| Method | Path                   | Description                 |
|--------|------------------------|-----------------------------|
| GET    | /api/templates         | List user's templates       |
| POST   | /api/templates         | Create template             |
| PUT    | /api/templates/:id     | Update template             |
| DELETE | /api/templates/:id     | Delete template             |
| PUT    | /api/templates/reorder | Update display order        |

### Post
| Method | Path       | Description                              |
|--------|------------|------------------------------------------|
| POST   | /api/post  | Compose and submit post to Bluesky       |

### Feed
| Method | Path                  | Description                                        |
|--------|-----------------------|----------------------------------------------------|
| GET    | /api/feed/following   | User's Following feed (cursor-paginated)           |
| GET    | /api/feed/hashtags    | Merged hashtag feed; accepts `tags` query param    |
| GET    | /api/feed/recent-tags | Last 10 unique hashtags posted by the user         |

### Web UI (HTMX)
| Method | Path           | Description                                          |
|--------|----------------|------------------------------------------------------|
| GET    | /              | Composer + template selector + feed                  |
| GET    | /templates     | Template management page                             |
| GET    | /settings      | Account settings (includes theme selector for paid)  |

---

## Project Structure

```
drafsky/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── auth/          # AT Protocol OAuth handlers and token management
│   ├── bluesky/       # indigo client wrapper, post construction, facets
│   ├── feed/          # Following and hashtag feed clients
│   ├── db/
│   │   ├── queries/   # .sql files (sqlc source)
│   │   └── sqlc/      # generated Go code (do not edit manually)
│   ├── handlers/      # Gin route handlers
│   ├── middleware/    # Auth middleware, session checking
│   └── models/        # Shared types not generated by sqlc
├── migrations/        # golang-migrate SQL migration files
├── templates/         # Go html/template files (.html)
├── static/            # CSS, minimal JS
├── db/
│   └── sqlc.yaml      # sqlc configuration
├── CLAUDE.md
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

### Naming
- Handlers: `HandleGetTemplates`, `HandleCreateTemplate` etc. (Handle + HTTP verb + resource)
- sqlc query files: one file per domain (`users.sql`, `templates.sql`)
- Migration files: `000001_create_users.up.sql` / `000001_create_users.down.sql`

### Database
- Always use parameterised queries (sqlc enforces this)
- Migrations are sequential and never edited after creation — add a new migration to fix mistakes
- The `did` column is the user identifier in all foreign key relationships, not `handle`
- Pending migrations before UI work: `000004_create_post_history`, `000005_add_theme_to_users`

### AT Protocol / Bluesky
- Always use the indigo library for post construction — never build `app.bsky.feed.post`
  records manually
- Token refresh must be handled transparently — check expiry before every API call
- Respect Bluesky rate limits; surface errors clearly to the user rather than silently failing

### HTMX
- Partial templates live in `/templates/partials/`
- HTMX responses return only the relevant partial, not a full page
- Keep `hx-` attributes in the HTML; do not drive HTMX behaviour from JavaScript

---

## Environment Variables

```
DATABASE_URL        PostgreSQL DSN
SESSION_SECRET      Random 32-byte secret for session signing
OAUTH_CLIENT_ID     AT Protocol OAuth client ID
OAUTH_REDIRECT_URL  Full callback URL (e.g. https://drafts.ky/auth/callback)
APP_ENV             development | production
PORT                HTTP listen port (default 8080)
```

---

## Phases

| Phase | Scope                                                                               |
|-------|-------------------------------------------------------------------------------------|
| 1     | Core API + AT Protocol OAuth + Go/HTMX web UI + Following feed + merged hashtag feed |
| 2     | Harden for public release: rate limiting, token refresh, error UX; add per-hashtag tabbed feed toggle |
| 3     | SwiftUI iOS app (same /api/* endpoints)                                             |
| 4     | Polish: template sharing, starter template packs                                    |

---

## Key Dependencies

```
github.com/gin-gonic/gin
github.com/jackc/pgx/v5
github.com/bluesky-social/indigo
github.com/golang-migrate/migrate/v4
sqlc (CLI tool — see https://sqlc.dev)
```

---

## UI Design

### Layout
Three-column layout matching the Bluesky/Twitter convention:

```
┌──────────────┬─────────────────────┬──────────────┐
│   Left Rail  │     Centre Feed     │  Right Rail  │
│              │                     │              │
│  Avatar      │  [post cards]       │ Recent Tags  │
│  Handle      │                     │ #NJDevils    │
│  Home        │                     │ #NHL         │
│  Templates   │                     │ #motosky     │
│  Settings    │                     │ ...          │
│  Logout      │                     │              │
│              │                     │              │
│  [New Post]  │                     │              │
└──────────────┴─────────────────────┴──────────────┘
```

- **Left rail:** Avatar, handle, nav links, New Post button at the bottom of the nav
- **Centre feed:** Following feed on load; switches to merged hashtag feed after posting
- **Right rail:** Last 10 unique hashtags the authenticated user has personally posted,
  pulled from `post_history`, ordered by most recently used. Fewer than 10 shown if the
  user hasn't posted 10 distinct hashtags yet.

### Composer (modal/popup)
Triggered by the New Post button. Modelled on the Bluesky compose modal:
- User avatar top-left
- Text area with placeholder
- Template selector dropdown (replaces the "Anyone can interact" area from Bluesky)
- Character counter (300 limit)
- Post button top-right
- Cancel top-left

### Colour Palette — Deep Ocean

| Role         | Hex       | Usage                                      |
|--------------|-----------|--------------------------------------------|
| Background   | `#070d1a` | Page background                            |
| Surface      | `#0d1829` | Left rail, right rail, card backgrounds    |
| Card         | `#132038` | Post cards, modal background               |
| Accent       | `#34d399` | Hashtags, active nav, buttons, links       |
| Text         | `#dbeafe` | Primary text                               |
| Muted        | `#4b6080` | Secondary text, timestamps, labels         |

CSS variables must be used throughout — never hardcode hex values in component styles.
Define all colours in a `:root {}` block in `/static/style.css`.

### Themes

Themes are sets of CSS variable overrides. The user's theme is injected as a class on
the `<body>` tag by the Go template, based on the `theme` column in the `users` table.
Free users are locked to `ocean`. Paid users can select any theme from their settings.

| Theme key  | Name              | Plan     | Accent    | Base                    |
|------------|-------------------|----------|-----------|-------------------------|
| `ocean`    | Deep Ocean        | Free     | `#34d399` | `#070d1a` / `#0d1829`  |
| `slate`    | Midnight Slate    | Paid     | `#7c9ef8` | `#0f1117` / `#181c27`  |
| `amber`    | Charcoal & Amber  | Paid     | `#f59e0b` | `#111111` / `#1a1a1a`  |
| `graphite` | Graphite & Sky    | Paid     | `#38bdf8` | `#131416` / `#1c1f23`  |

**Implementation rules:**
- `/static/style.css` defines `:root` with the `ocean` defaults
- Each paid theme is a `body.slate { }`, `body.amber { }`, `body.graphite { }` block
  that overrides only the CSS variables that differ
- The base Go layout template sets `<body class="{{ .User.Theme }}">`
- A free user whose `theme` column is somehow set to a paid theme must fall back to
  `ocean` — enforce this in the template data layer, not in CSS
- Adding a new theme in future requires only a new CSS block and a new row in this table

### Typography
System font stack — no external font dependencies for v1.

---

## Monetisation

DraftSky uses a freemium model on web and an ad-supported + one-time purchase model on iOS.

**Web (future):**
- Free tier: up to 5 templates, Following feed, basic posting, Ocean theme only
- Paid tier: unlimited templates, tabbed hashtag feed (Phase 2), all themes
- Enforced via the `plan` column on the `users` table

**iOS (Phase 3):**
- Ads shown by default (AdMob or equivalent)
- Non-consumable IAP via StoreKit 2 to remove ads permanently
- Purchase is tied to the user's DID, not the device — buying on iOS sets `plan = 'paid'`
  server-side, removing ads on both iOS and web
- **Server-side Apple receipt verification is mandatory** — never trust the client to
  self-report a successful purchase. Verify via Apple's API before updating `plan`
- Apple Small Business Program (15% vs 30% cut) — apply at launch

**Architecture note:**
The `plan` column is already in the schema. No handler currently checks it — add
enforcement when freemium tiers are introduced. A middleware helper `RequirePaidPlan`
should be added at that point, following the same pattern as `RequireAuth`.

---

## Out of Scope (for now)

- Multi-platform support (Mastodon, Threads etc.) — Bluesky only
- Scheduling posts
- Analytics or engagement tracking
- Team/shared template libraries (Phase 4 consideration)
- Dark mode (add later — ship first)
