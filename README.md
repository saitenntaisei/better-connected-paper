# Better Connected Paper

A visual literature-exploration tool inspired by [Connected Papers](https://www.connectedpapers.com/),
with one key improvement: **edges are directed** so you can see *which
paper cites which* at a glance.

Connected Papers renders similarity as an undirected graph, which hides
the single most important signal in academic literature: who builds on
whom. Better Connected Paper keeps the familiar force-directed layout
but draws citation links as arrows (A→B means "A cites B") on top of an
optional similarity overlay.

---

## Features

- **Directed citation graph** (arrows show cite direction)
- **Similarity overlay** (toggle on to see bibliographic-coupling / co-citation / direct-link edges)
- **Year-gradient node color** (older papers blue → newer papers red)
- **Citation-count node sizing** (log-scaled)
- **Shareable URLs** (`?seed=<paperId>` rehydrates the graph)
- **Postgres cache** (same seed returns in <200 ms on a warm DB)
- **Two-pane UI**: seed search + results + graph + per-paper detail panel

---

## Architecture

```
                ┌─────────────────────┐      HTTPS       ┌────────────────────┐
 Browser        │  React + Vite SPA   │ ───────────────▶ │   Go (chi router)  │
 (Cytoscape.js) │  cose-bilkent       │   /api/search    │                    │
                │  directed arrows    │   /api/graph/*   │  citation.Client   │
                │  year color /size   │   /api/paper/*   │  graph.Builder     │
                └─────────────────────┘                  │  store.DB (pgx)    │
                                                         └──────────┬─────────┘
                                                                    │
                                                   ┌────────────────┼────────────────┐
                                                   ▼                                 ▼
                                      ┌──────────────────────┐         ┌──────────────────────┐
                                      │ OpenAlex (default)   │         │  Postgres (Neon /    │
                                      │ or Semantic Scholar  │         │  docker compose)     │
                                      │ search / work / batch│         │  papers, edges,      │
                                      │                      │         │  graph_cache (TTL 30d)│
                                      └──────────────────────┘         └──────────────────────┘
```

- `backend/internal/citation` — OpenAlex (primary), OpenCitations (secondary supplement), and Semantic Scholar (opt-in) clients with rate limiting + retries; selected by `CITATION_PROVIDER` / `CITATION_SECONDARY` / `CITATION_TERTIARY`
- `backend/internal/graph` — 2-hop expansion, similarity scoring, directed edge construction
- `backend/internal/store` — pgx pool + embedded migrations + cache layer
- `backend/internal/api` — chi router + HTTP handlers
- `backend/cmd/server` — local/docker entry (`PORT` listener)
- `backend/api/server.go` — Vercel Go Function entry
- `frontend/src/components/Graph.tsx` — Cytoscape + cose-bilkent render
- `frontend/src/lib/graphElements.ts` — pure node/edge transform (unit-tested)

### How the graph is built

```
Input: seed paper S
1. Fetch S (metadata, references, citations) from the configured provider (OpenAlex by default)
2. Pool P = refs(S) ∪ cites(S)   (first-hop neighbors)
3. Batch-fetch metadata + refs + cites for every p ∈ P     (one /paper/batch call)
4. Score each p:
     sim(S, p) = 0.4 · bibliographic_coupling(S, p)
               + 0.4 · co_citation(S, p)
               + 0.2 · direct_link(S, p)
5. Keep the top 40 nodes (seed always included).
6. Directed edges: for every pair (a, b) in the kept set,
     if a ∈ refs(b) → add edge a→b  (kind = "cite")
   Similarity edges (kind = "similarity") are computed separately and
   the frontend toggles their visibility.
7. Persist as JSON blob keyed by seedId; TTL 30 days.
```

---

## Tech stack

| Layer           | Choice                                          | Why                                             |
| --------------- | ----------------------------------------------- | ----------------------------------------------- |
| Backend         | Go 1.25 · [chi](https://github.com/go-chi/chi) · [pgx/v5](https://github.com/jackc/pgx) | fast, stdlib-compatible, great Postgres story  |
| Migrations      | [golang-migrate](https://github.com/golang-migrate/migrate), embedded | reproducible, idempotent on cold-start          |
| Rate limiting   | `golang.org/x/time/rate`                        | respects OpenAlex (10 req/s), OpenCitations (polite pool), and opt-in Semantic Scholar (1 req/3s anon) budgets |
| Frontend        | React 19 · TypeScript 5.7 · Vite 6             | fastest DX, matches Vercel defaults             |
| Graph           | [Cytoscape.js](https://js.cytoscape.org/) · [cose-bilkent](https://github.com/cytoscape/cytoscape.js-cose-bilkent) | directed arrows built in, good up to ~1k nodes  |
| Tests           | `go test` · Vitest 3 · @testing-library/react  | same shape on both sides                        |
| Dev orchestration | [mise](https://mise.jdx.dev) + docker-compose | one-command setup                               |
| Deploy          | Vercel (2 projects) + Neon Postgres             | zero-ops, preview URLs per PR                   |

---

## Quick start

```bash
cp .env.example .env        # default provider is OpenAlex; set OPENALEX_EMAIL for the polite pool
                            # or CITATION_PROVIDER=semanticscholar + SEMANTIC_SCHOLAR_API_KEY
mise install                # Go 1.25 + Node 22
mise run dev                # docker compose: postgres + backend + frontend
# open http://localhost:5173
```

Useful tasks:

```bash
mise run test               # backend `go test` + frontend vitest
mise run lint               # go vet + gofmt check + eslint
mise run build              # production builds on both sides
mise run migrate            # apply Postgres migrations manually
mise run dev:backend        # run just the Go server (needs a running DB)
mise run dev:frontend       # run just the Vite dev server
```

The Vite dev server proxies `/api/*` to `http://localhost:8080`, so the
frontend talks to the Go server with no CORS dance.

---

## HTTP API

All JSON, served under `/api`. See `frontend/src/types/api.ts` for the
exact TypeScript shapes.

### `GET /api/health`

```json
{ "status": "ok", "time": "2026-04-18T12:34:56Z" }
```

### `GET /api/search?q=<query>&limit=<n>`

Proxies the configured provider's paper search (OpenAlex `/works?search=` by default, or Semantic Scholar). `limit` defaults to 10.

```json
{
  "total": 4321,
  "results": [
    { "id": "649def...", "title": "Attention Is All You Need",
      "year": 2017, "authors": ["Ashish Vaswani", "..."],
      "citationCount": 123456, "venue": "NeurIPS" }
  ]
}
```

### `GET /api/paper/{id}`

Full metadata for one paper (title, authors, year, venue, abstract,
DOI, external URLs, citation/reference counts). 404 if the paper is
unknown to the configured provider.

### `POST /api/graph/build`

Builds (or serves cached) directed citation graph around a seed.

```http
POST /api/graph/build
Content-Type: application/json

{ "seedId": "649def...", "fresh": false }
```

Response (also used by the frontend cache):

```json
{
  "seed": { "id": "649def...", "title": "...", "year": 2017, "...": "..." },
  "nodes": [
    { "id": "...", "title": "...", "year": 2015, "citationCount": 987,
      "similarity": 0.82, "isSeed": false }
  ],
  "edges": [
    { "source": "a", "target": "b", "kind": "cite" },
    { "source": "a", "target": "c", "kind": "similarity", "weight": 0.63 }
  ],
  "builtAt": "2026-04-18T12:34:56Z"
}
```

Response headers: `X-Cache: hit|miss`.

### `GET /api/graph/{seedId}`

Cache-only lookup. Returns 404 if the seed has never been built
(i.e., the client should `POST /api/graph/build` first).

---

## Tests

```bash
mise run test               # unit: Go + Vitest
mise run test:integration   # requires a running Postgres (mise run dev)
```

- `backend/internal/citation` — httptest mock for 200 / 429 / 5xx paths
- `backend/internal/graph`    — table-driven similarity + edge direction checks
- `backend/internal/api`      — handler tests with fake `Deps`
- `backend/internal/store`    — testcontainers-backed integration tests (gated by `-tags=integration`)
- `frontend/src/lib/graphElements.test.ts` — pure transform tests
- `frontend/src/components/*.test.tsx` — @testing-library render + interaction
- `frontend/src/hooks/*.test.tsx` — search / URL-sync behavior

---

## Deployment

See [DEPLOYMENT.md](DEPLOYMENT.md) for the two-Vercel-project layout,
required environment variables, and deploy commands.

---

## Project layout

```
.
├── mise.toml                          tool pins + task runner
├── docker-compose.yml                 postgres + backend + frontend (local)
├── .env.example
├── DEPLOYMENT.md
├── backend/
│   ├── cmd/server/                    local entry (PORT listener)
│   ├── api/server.go                  Vercel Go Function entry
│   ├── internal/
│   │   ├── api/                       chi router + handlers
│   │   ├── citation/                  OpenAlex primary + OpenCitations secondary (+ opt-in S2 tertiary)
│   │   ├── graph/                     builder + similarity
│   │   └── store/                     pgx + migrations + cache
│   └── Dockerfile
└── frontend/
    ├── src/
    │   ├── App.tsx
    │   ├── api/client.ts              typed fetch wrapper
    │   ├── components/                SearchBar / Graph / PaperDetail / Legend / ...
    │   ├── hooks/                     useSearch / useGraph / useUrlSeed
    │   ├── lib/                       pure graph transforms + styles
    │   └── types/api.ts               mirrors backend JSON
    ├── vite.config.ts                 dev proxy /api → :8080
    └── Dockerfile
```

---

## Roadmap

- Playwright E2E (search → graph → detail happy path)
- Multi-seed graphs (compare two papers side-by-side)
- Embedding-based similarity as a third overlay
- User-saved collections

---

## License

MIT — see [LICENSE](LICENSE).
