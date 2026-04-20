# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

A literature-map tool that renders a **directed** 2-hop citation graph around a seed paper (A → B means "A cites B"), with a toggleable undirected similarity overlay. The differentiator vs Connected Papers is that citation edges are drawn as arrows; similarity is the optional view.

## Common commands

`mise.toml` is the task runner — always prefer `mise run <task>` over invoking the underlying tools directly, so env (`.env`) and working directories (`dir = "backend"` / `"frontend"`) are applied consistently.

```bash
mise install                  # Go 1.25 + Node 22 per mise.toml
mise run dev                  # docker compose: postgres + backend + frontend
mise run dev:down             # stop the stack
mise run dev:backend          # run Go server against an already-running postgres
mise run dev:frontend         # Vite dev server
mise run migrate              # apply migrations then exit (-migrate-only flag)

mise run test                 # backend go test + frontend vitest
mise run test:integration     # DB-backed store tests; needs a running postgres + DATABASE_URL
mise run test:backend         # just `go test ./...` in backend/
mise run test:frontend        # `npm test -- --run` in frontend/

mise run lint                 # go vet + gofmt -l check, then `npm run lint`
mise run build                # go build + `npm run build`
```

Run a single Go test: `cd backend && go test ./internal/graph -run TestRankCandidates`.
Run a single frontend test: `cd frontend && npm test -- src/lib/graphElements.test.ts`.
Integration tests are gated by the `integration` build tag (`//go:build integration`) and skip when `DATABASE_URL` is unset, so the default `go test ./...` never touches a DB.

## Backend architecture

### Two entry points sharing `internal/*`

- **`backend/cmd/server/main.go`** — the long-running HTTP server used by `docker compose` and local dev. Graceful shutdown, 300 s write timeout for slow graph builds.
- **`backend/api/server.go`** — Vercel Go Runtime `Handler`. `vercel.json` rewrites every `/api/*` to this single function; `initHandler` wires the DB + citation client once via `sync.Once` so cold starts pay setup cost once per instance.

**The citation-provider wiring (`newPaperClient` + `newHybridSecondary` + `newHybridTertiary`) is duplicated between these two files.** When you change provider defaults, env-var names, or hybrid chain composition, change both.

### Citation provider chain (`internal/citation`)

Defaults to a `HybridClient` with OpenAlex as primary and OpenCitations as secondary. The tertiary slot is **off by default** — Semantic Scholar's 1 req/3 s anonymous rate limit caused 429 cascades on cold graph builds, so it's opt-in via `CITATION_TERTIARY=semanticscholar` (and only really safe with an API key).

Key non-obvious behavior:
- `HybridClient.trySupplement` walks secondary → tertiary and a layer only "wins" if `supplementNarrowedGap` is true (strictly more refs, strictly more cites, or resolves `CitationsUnknown`). A low-coverage secondary doesn't short-circuit a tertiary that would have filled the real gap.
- `HybridClient.arxivSiblingLookupID` probes OpenAlex for a same-title arxiv-DOI sibling before falling back to the noisier title search. This is what surfaces recent robotics preprints (OpenVLA, π₀, DROID, CrossFormer, RDT-1B) that live under arxiv DOIs S2 indexes but OpenAlex's conference DOI doesn't reach.
- `HybridClient.supplementBatchRefs` fires **one** tertiary batch call with DOI-resolved IDs to fill arxiv preprints' empty refs in the first-hop fetch — missing this collapses biblio-coupling to 0 for the modern preprint cluster.
- `Paper.CitationsUnknown` means "provider capped cites, don't treat empty list as authoritative". The builder's `persistFetched` skips `ReplacePaperLinks` on papers with this flag — otherwise a cache warm read would return an empty cite slice and corrupt scoring.
- `Paper.MergedFromID` is the secondary's paperId when a hybrid merge happened. `graph.canonicalizeSeedAlias` rewrites refs/cites lists so the seed doesn't materialize as a duplicate node when the secondary's first-hop papers point at the alias.

### Graph build pipeline (`internal/graph/builder.go`)

Staged fetch budgeted for Vercel's 60 s function cap:

1. `fetchSeed` — cache first (requires refs/cites to be persisted), else full `seedFields` from S2.
2. First-hop: `minimalLinkFields` (paperId + counts + ref/cite IDs only) via `fetchWithCache`. Capped at `MaxFirstHop` = 300.
3. 2-hop bridges via `countTwoHopSupport` → `selectBridgeIDs` (top N by support, `MaxBridgeCandidates` = 200), fetched with `bridgeLinkFields` (refs only — cites enrichment deliberately skipped).
4. `rankCandidates` scores all hydrated candidates with Connected-Papers-style `(biblio + coCite)/2` on Salton normalization. `CoCitationApprox` reconstructs the co-citation numerator from first-hop refs so bridges and direct neighbors compete on one scale. Trim to `MaxNodes` − 1 = 39.
5. Full metadata fetch (`fullNodeFields`) for the selected top-N only; persist via `Cache.UpsertPapers` + `ReplacePaperLinks`.
6. `buildCiteEdges` for every selected-pair citation; `buildSimilarityEdges` with threshold 0.08 (lowered from 0.15 because OpenAlex returns `referenced_works_count=0` for many arxiv preprints, halving biblio scores). `pruneOrphanNodes` drops non-seed nodes with zero incident edges.

`SimilarityEdgeThreshold = 0.08` and the preprint-cluster reasoning above are load-bearing — raising it silently drops legitimate bridge papers out of the visual graph.

### Cache layer (`internal/store`)

- `store.DB` satisfies `graph.Cache` via `papers` + `paper_links` tables and persists built graphs under `graphs` with a 30-day TTL (`DefaultGraphTTL`).
- **All `DB` methods tolerate a nil receiver** so the whole thing compiles out cleanly when `DATABASE_URL` is unset. `graphCache(db)` in both entry points returns `graph.Cache(nil)` rather than a typed-nil interface so nil checks inside `Builder` work.
- `Migrate()` runs embedded `migrations/*.sql` idempotently on boot. Migrations named `NNN_reset_*.sql` are deliberate cache invalidations; when you change ranking/scoring semantics, add a new numbered reset migration so stale cached graphs don't resurface.

### `api` package wiring

- `NewRouter(deps)` auto-provisions `deps.SFlight` and `deps.SearchCache` when they're nil, so tests and production share the same code path.
- `Deps` fields are all nullable; handlers self-describe missing deps via 503 (e.g. `BuildGraph` returns "graph builder unavailable" when `d.Builder == nil`).
- `resolveAllowedOrigins` reads `ALLOWED_ORIGINS` (comma-separated). Empty → `localhost:5173`/`3000` defaults. A deployed frontend without `ALLOWED_ORIGINS` set will hit CORS failures.

## Frontend architecture

- **`frontend/src/lib/graphElements.ts`** is a pure transform from `GraphResponse` → Cytoscape `ElementDefinition[]` with a standalone test (`graphElements.test.ts`). Keep the transform pure so the node/edge mapping stays unit-testable independent of Cytoscape's DOM layer.
- **`frontend/src/api/client.ts`** — `DEFAULT_BASE` reads `VITE_API_BASE` at build time and falls back to the relative `/api`. In docker-compose the browser still hits `/api` relative and Vite's proxy (configured by `BACKEND_URL` in `vite.config.ts`, **not** `VITE_API_BASE`) forwards to the backend container. `BACKEND_URL` is deliberately not `VITE_*` prefixed so it can't leak into the client bundle.
- **`frontend/src/types/api.ts`** mirrors the Go JSON shapes. When you change backend response fields, update both — the client's tests catch divergence.
- **URL state** — `hooks/useUrlSeed.ts` is the canonical source of the seed id; `?seed=...` rehydrates the full graph for a collaborator.

## Deployment notes

Two Vercel projects (`bcp-backend`, `bcp-frontend`) on the same repo; see `DEPLOYMENT.md`. Postgres is Neon via Vercel Marketplace, which injects `DATABASE_URL` automatically. The Go function's `maxDuration: 60` is a safety cap — a cold build with many supplement calls can approach it on a new seed.

## Conventions worth following

- **No new doc files unless asked.** README.md, DEPLOYMENT.md, and this file already cover the territory; don't create planning/analysis docs for intermediate work.
- **Keep backend comments load-bearing.** Existing comments on `CoCitationApprox`, `SimilarityEdgeThreshold`, and the hybrid supplement chain encode *why* the defaults are what they are — don't strip them.
- **Reset migrations over in-place edits.** When scoring or graph shape changes, add `NNN_reset_*.sql` rather than mutating an existing migration file.
- **Mirror provider-wiring changes in both entry points.** `cmd/server/main.go` and `api/server.go` duplicate `newPaperClient` intentionally; drift here means local dev and Vercel behave differently.
