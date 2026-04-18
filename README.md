# Better Connected Paper

A visual literature-exploration tool inspired by [Connected Papers](https://www.connectedpapers.com/), with one key improvement: **edges are directed** so you can see *which paper cites which*.

- **Data source**: Semantic Scholar Academic Graph
- **Backend**: Go + chi + pgx (Postgres)
- **Frontend**: React + TypeScript + Vite + Cytoscape.js
- **Tooling**: `mise` for Go / Node version pinning and task running
- **Local dev**: `mise run dev` brings up Postgres + backend + frontend via Docker Compose
- **Deploy**: Vercel Services (Go backend + Vite site) + Neon (Postgres via Vercel Marketplace)

## Quick start

```bash
cp .env.example .env        # optionally fill SEMANTIC_SCHOLAR_API_KEY
mise install                # installs Go 1.24 + Node 22
mise run dev                # docker compose up
# open http://localhost:5173
```

More details land in later commits — see `docs/` once populated.

## License

MIT — see [LICENSE](LICENSE).
