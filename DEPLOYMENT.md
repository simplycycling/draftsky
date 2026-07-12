# DraftSky — Railway Deployment

## Prerequisites

- A Railway project with a **PostgreSQL** addon attached
- The custom domain `draftsky.social` pointed at the Railway service
- Docker installed locally (for pre-deploy verification)

---

## Environment Variables

Set these in the Railway service's **Variables** panel before deploying.

| Variable           | Value / Notes                                                    |
|--------------------|------------------------------------------------------------------|
| `DATABASE_URL`     | Set automatically by the Railway PostgreSQL addon — copy the **Internal** connection string from the addon's **Connect** tab |
| `SESSION_SECRET`   | Random 32-byte secret — see generation command below            |
| `OAUTH_CLIENT_ID`  | Must be exactly `https://draftsky.social/client-metadata.json`  |
| `OAUTH_REDIRECT_URL` | Must be exactly `https://draftsky.social/auth/callback`       |
| `APP_ENV`          | `production`                                                     |
| `PORT`             | Set automatically by Railway — do not override                   |
| `ADMIN_DID`        | Optional — the owner's DID (e.g. `did:plc:...`). Gates `GET /admin/stats`; unset leaves the route 404ing for everyone |

### Generating SESSION_SECRET

```sh
openssl rand -hex 32
```

Paste the output as the `SESSION_SECRET` value.

---

## Database Migrations

Migrations are not run automatically at startup. Run them once manually after provisioning
the database, and again whenever new migration files are added.

Install the `migrate` CLI if not already present:

```sh
brew install golang-migrate
```

Run all pending migrations against the Railway database:

```sh
migrate -path ./migrations -database "$DATABASE_URL" up
```

Replace `$DATABASE_URL` with the Railway PostgreSQL connection string (use the
**External** URL when running from your local machine).

> **This deploy:** migration `000008_add_last_seen_to_users` adds the `last_seen_at`
> column that powers the admin activity stats (DAU/WAU/MAU). It MUST be run against
> production on this deploy or the auth middleware's activity-touch write will error
> (logged, non-fatal) and `/admin/stats` will 500.

---

## AT Protocol OAuth Registration

The AT Protocol requires the `client_id` URL to resolve to a valid client metadata
document. Once the domain is live, verify the endpoint returns the correct JSON:

```sh
curl https://draftsky.social/client-metadata.json
```

The response must include `"client_id": "https://draftsky.social/client-metadata.json"`.
This is served automatically by the app at `GET /client-metadata.json` when
`OAUTH_CLIENT_ID` is set to that URL.

---

## Deploying

1. Push to the Railway-connected git branch (or use `railway up` from the CLI).
2. Railway builds the image using the `Dockerfile` at the project root.
3. The service starts with `/app/draftsky`.

To verify the image builds cleanly before pushing:

```sh
docker build -t draftsky .
```

---

## Rollback

Railway keeps previous deployments. Use the Railway dashboard to redeploy an earlier
build if a deployment needs to be rolled back.
