# Plan: performance + multi-user readiness

Goal: make benreadin fast and safe for many concurrent users, not just the site
owner. Eight items: five performance/fairness, two UX, one operability.

## Performance

### 1. Fix SQLite pragmas (WAL was never on) + allow concurrent reads
`internal/db/db.go` opens the DB with `?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000`
— that is **mattn/go-sqlite3 syntax**. `modernc.org/sqlite` silently ignores it;
verified empirically: the app runs with `journal_mode=delete`, `busy_timeout=0`.
- Switch DSN to `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=synchronous(normal)`.
- Drop `SetMaxOpenConns(1)` → pool of 8. Today every cache read across all
  concurrent searches serializes on one connection; WAL + busy_timeout make a
  reader pool safe (writes still serialize inside SQLite).
- Regression test: open a DB via `db.Open`, assert `PRAGMA journal_mode` = `wal`.

### 2. Batch book-cache reads
`search.go` phase 2 issues one `SELECT` per book (300-book shelf = 300 round
trips before the first cached card is sent). Add `CacheService.GetBooks(ids,
libsKey)` — single `IN (...)` query returning a map — and use it in the handler.
Unit test with an in-memory DB.

### 3. Server-side Goodreads shelf cache (5-min TTL)
Every search re-fetches the shelf RSS (up to 5 pages) even when two users open
the same shared shortlink seconds apart. The `shelf_cache` table already exists
but is dead code (only ever purged, never read/written). Repurpose it: cache the
parsed `[]models.Book` keyed `userID:shelf`, TTL 5 min, `refresh=true` bypasses.
Implement inside `GoodreadsService` (gets the `CacheService` injected) so all
three call sites benefit: search handler, prewarm, recommendations.

### 4. Request coalescing (singleflight)
Two concurrent identical searches — same shelf via a shared link, or prewarm
overlapping a live search — currently duplicate every upstream call. Wrap with
`golang.org/x/sync/singleflight` (already a dependency tree member):
- `OverDriveService.CheckAvailability` keyed `libraryKey|query`
- `GoodreadsService.FetchShelf` keyed `userID|shelf`

### 5. Per-search OverDrive fairness cap
The global `CONCURRENCY_OVERDRIVE=50` semaphore is first-come-first-served: one
1000-book shelf occupies all 50 slots and starves every other user. Add a
per-request semaphore (default 16, env `CONCURRENCY_OVERDRIVE_PER_SEARCH`)
acquired *before* the global one, so a single search can hold at most 16 slots.

## UX

### 6. Actionable Goodreads error messages
Today a private profile surfaces as `Failed to fetch Goodreads shelf: fetch
goodreads page 1: goodreads returned 404`. Return a typed status error from
`goodreads.fetchPage` and map it in `FetchShelf`:
- 404 → "No Goodreads user with that ID, or their shelf is private. Check the
  ID, and make sure the profile is public (Goodreads → Settings → Privacy →
  'Anyone')."
- 401/403 → private-profile instructions
- 429 → "Goodreads is rate-limiting requests — wait a minute and try again."
Unit-test the mapping.

### 7. Shelf picker on the home page
Only `to-read` is reachable unless the user hand-crafts a shelf URL. Add a
`shelf` select to `index.html` (to-read / currently-reading / read / custom
name), a `shelf` query param that overrides the parsed shelf in
`handlers/search.go` + `handlers/recommendations.go`, carry it through
`search.js` (submit + last-search persistence) and `results.js` (`baseParams`
so Refresh/recs/shortlinks keep the shelf).

## Operability

### 8. Prometheus /metrics
Can't operate a multi-user service blind. Add `prometheus/client_golang`
(pure Go, CGo-free) with a small, deliberate metric set in a new
`internal/metrics` package:
- `benreadin_searches_total{outcome=ok|error}`
- `benreadin_books_checked_total`
- `benreadin_cache_requests_total{cache=book|library|shelf, result=hit|miss}`
- `benreadin_upstream_errors_total{service=goodreads|overdrive|openlibrary|amazon}`
- `benreadin_active_streams` gauge
- `benreadin_shelf_fetch_duration_seconds` histogram
Mount `promhttp` at `/metrics`.

## Files touched
`internal/db/db.go`, `internal/db/db_test.go` (new), `internal/services/cache.go`
(+test), `internal/services/goodreads.go` (+test), `internal/services/overdrive.go`,
`internal/services/recommendations.go`, `internal/handlers/search.go`,
`internal/handlers/recommendations.go`, `internal/metrics/metrics.go` (new),
`cmd/benreadin/main.go`, `.env.example`, `public/index.html`, `public/js/search.js`,
`public/js/results.js`, `CLAUDE.md` (shelf-cache + metrics notes), `go.mod`.

## Verification
- `mise run lint && mise run test` (new tests: WAL pragma, GetBooks batch,
  Goodreads error mapping, shelf cache TTL).
- Launch locally; `curl /metrics` shows counters moving after a search.
- SSE smoke: `curl -sN '/api/search?url=97106512-william&libraries=nypl&shelf=read'`
  returns the read shelf; second run within 5 min skips the RSS fetch (log line).
- Playwright: pick "Read" shelf on the home page, confirm results page carries
  `shelf=read` and Refresh preserves it.
