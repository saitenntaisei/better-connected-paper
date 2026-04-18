# Deployment

This repo ships two independent Vercel projects that share a single Git
repository — the Go backend (as a serverless Function) and the Vite + React
frontend (as a static site). Postgres is provisioned via the Vercel
Marketplace (Neon).

## Project layout

| Vercel project | Root Directory | Framework | What it serves           |
| -------------- | -------------- | --------- | ------------------------ |
| `bcp-backend`  | `backend/`     | Other     | `/api/*` (Go Handler)    |
| `bcp-frontend` | `frontend/`    | Vite      | Static SPA + asset CDN   |

Each project has its own `vercel.json`:

- `backend/vercel.json` — declares the `api/server.go` Go Function (512 MB,
  60 s) and rewrites `/api/(.*)` to `/api/server`, so a single entry point
  handles every `/api/*` route while the chi router inside still sees the
  original URL path.
- `frontend/vercel.json` — rewrites every non-asset path to `/index.html`
  so the SPA can own client-side routing.

The Go entry point is `backend/api/server.go`. It exposes a `Handler`
function that Vercel's Go Runtime calls for each request. Dependencies
(Postgres pool, Semantic Scholar client, graph builder) are lazily wired
via `sync.Once` so the cold-start cost is paid once per instance.

## One-time setup

1. **Install the CLI** if you don't have it:

   ```bash
   npm i -g vercel
   ```

2. **Link the backend project**:

   ```bash
   cd backend
   vercel link            # pick "Create new project" → name it bcp-backend
   ```

3. **Link the frontend project**:

   ```bash
   cd ../frontend
   vercel link            # pick "Create new project" → name it bcp-frontend
   ```

4. **Provision Postgres** via Vercel Marketplace → Neon on the backend
   project. Accept the default integration — it injects `DATABASE_URL`
   (plus `POSTGRES_URL_NON_POOLING` etc.) into the backend environment.

5. **Add the remaining secrets**:

   ```bash
   # backend
   cd backend
   vercel env add SEMANTIC_SCHOLAR_API_KEY   # optional but strongly recommended
   # DATABASE_URL is provided by the Neon integration — no manual add needed

   # frontend
   cd ../frontend
   vercel env add VITE_API_BASE              # e.g. https://bcp-backend.vercel.app/api
   ```

   Use `Production`, `Preview`, and `Development` scopes as appropriate.
   `VITE_API_BASE` is read at build time by `frontend/src/api/client.ts`
   and falls back to `/api` if unset.

## Deploy

```bash
# backend
cd backend && vercel deploy --prod

# frontend
cd ../frontend && vercel deploy --prod
```

Preview deploys (no `--prod`) work the same way and each get their own
immutable URL. For preview traffic from the frontend, point
`VITE_API_BASE` to the corresponding backend preview URL or wire them
together via a [Vercel project-to-project rewrite].

[Vercel project-to-project rewrite]: https://vercel.com/docs/edge-network/rewrites

## Verify

After deploy:

- `curl https://<backend-url>/api/health` → `{"status":"ok", ...}`
- Load the frontend URL, search for a paper, confirm the graph renders.
- `vercel logs <backend-url>` should show requests hitting the Go
  function without panics. First request may take ~1–2 s (cold start +
  `Migrate()` running against Neon).

## Notes & gotchas

- The Go Function listens on Vercel's invocation contract, not on a
  `PORT`. `backend/cmd/server/main.go` is only used for local/docker dev.
- Migrations run on cold start (`db.Migrate(ctx)` inside `initHandler`).
  They are idempotent thanks to `golang-migrate`; if a migration fails
  the handler still serves `/api/health` so you can debug via logs.
- `maxDuration: 60` is a safety upper bound — the graph builder should
  finish well under 10 s for a warm cache; cold builds with many
  Semantic Scholar calls can approach 30 s on a new seed.
- If you later outgrow a single-file Function (e.g. want to split by
  route), replace `api/server.go` with multiple files under `api/` and
  drop the `rewrites` block.
