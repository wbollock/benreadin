# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

BenReadin: paste a Goodreads shelf URL (or OverReader URL), pick Libby/OverDrive libraries, and stream back per-book availability, Amazon prices, and free Project Gutenberg matches. Single Go binary serving a vanilla-JS frontend from `public/`; SQLite for caching only (no user data).

## Commands

Tasks are defined in `.mise.toml` (Go 1.25 via mise):

```sh
mise run dev       # go run ./cmd/benreadin  (loads .env via godotenv)
mise run build     # go build -o bin/benreadin ./cmd/benreadin
mise run lint      # go vet ./...
mise run test      # go test ./...
mise run migrate   # go run ./cmd/benreadin -migrate-only
```

Run a single test: `go test ./internal/services/ -run TestParseShelfURL`

CI (`.github/workflows/ci.yml`) runs vet, test, and a `CGO_ENABLED=0` build. The SQLite driver is pure-Go (`modernc.org/sqlite`) — keep it CGo-free.

## Architecture

The whole app is wired in `cmd/benreadin/main.go`: config from env vars (see `.env.example`), services constructed there and injected into handlers, chi router. To add a service or endpoint, follow that wiring pattern.

**Search flow** (the core path): `GET /api/search` in `internal/handlers/search.go` is an SSE stream consumed by `EventSource` in `public/js/results.js`.

1. `services/urlparse.go` normalizes the input URL (OverReader-style or raw Goodreads) into userId + shelf.
2. `services/goodreads.go` fetches the shelf via Goodreads RSS (paginated, no auth). A `book_stubs` SSE event ships the full book list immediately so the frontend renders placeholder cards up front.
3. Per book, fan-out under `semaphore.Weighted` limits (`CONCURRENCY_OVERDRIVE`, `CONCURRENCY_AMAZON`): OverDrive Thunder API availability per library (`services/overdrive.go`), Open Library ISBN-13/cover enrichment (`services/openlibrary.go`), Amazon PA-API pricing (`services/amazon.go`), Gutenberg catalog match (`services/gutenberg.go`). Each finished book is sent as a `book` SSE event; `progress` events track completion.

**Caching** is layered in SQLite (`internal/db/schema.sql`, helpers in `services/cache.go`), all TTL-enforced by comparing `fetched_at` at read time: `library_cache` (2h), `amazon_cache` (24h), `book_cache` (full BookEvent per goodreads_id + library set, 2h, read as one batched `IN` query per search), `shelf_cache` (parsed Goodreads shelf list per userID|shelf, 5min). Concurrent identical fetches are coalesced with `singleflight` (Goodreads shelves, OverDrive availability). Schema changes go in `schema.sql` — statements must stay idempotent (`IF NOT EXISTS` / `INSERT OR IGNORE`) since migrations re-run on every startup; column additions use `addColumnIfMissing` in `db.go` instead. The DSN must use modernc's `_pragma=name(value)` syntax — mattn-style params are silently ignored (there is a regression test for WAL).

**Other endpoints**: `/api/recommendations` (Open Library subject-based recs, `services/recommendations.go`), `/api/libraries` (autocomplete over the seeded `libraries` table), `/api/shorten` + `/s/{token}` (shareable search shortlinks; carry the shelf), `/metrics` (Prometheus, `internal/metrics` — restrict at the reverse proxy on public instances).

A `shelf` query param (home-page shelf picker) overrides the shelf implied by the pasted URL on `/api/search` and `/api/recommendations`; the frontend only sends it when it differs from the `to-read` default.

**Frontend** is framework-free: `public/index.html` + `js/search.js` (form → redirect), `public/results.html` + `js/results.js` (SSE consumer, filtering/sorting) + `js/bookCard.js` (card rendering). No build step — files are served as-is from `public/`.

## Constraints and conventions

- **Amazon is optional.** With no PA-API credentials the service is disabled and the UI degrades gracefully. Never make pricing a hard dependency. PA-API 5.0 is deprecated April 2026; the Amazon service interface is meant to be swappable for the Creators API.
- **SSE, not WebSockets.** The server sets `WriteTimeout: 0` deliberately — a write timeout would kill long-lived streams. Behind nginx, `proxy_buffering off` is required.
- The Gutenberg catalog (~2MB CSV) downloads in a background goroutine at startup and refreshes weekly; free-book matches are empty for the first minute on a fresh DB.
- External API clients set a `User-Agent` and respect rate limits (Open Library: 3 req/sec).
- Commit messages follow Conventional Commits (`feat(scope):`, `fix(ui):`, `chore(ops):` — see git log).
