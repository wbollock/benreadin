# BenReadin — Architecture Plan

## Overview

A web app inspired by OverReader. User pastes a Goodreads "to-read" shelf URL, the app
fetches every book, checks Libby/OverDrive availability across one or more libraries,
shows Amazon prices (Kindle, paperback, hardcover), and provides affiliate links.

---

## Tech Stack

| Concern         | Technology                                  |
|-----------------|---------------------------------------------|
| Language        | Go 1.22+                                    |
| HTTP router     | `net/http` stdlib + `chi`                   |
| SQLite driver   | `modernc.org/sqlite` (pure Go, no CGo)      |
| RSS parsing     | `github.com/mmcdole/gofeed`                 |
| Concurrency     | `golang.org/x/sync/errgroup` + `semaphore`  |
| Frontend        | Plain HTML + vanilla JS + CSS (no framework)|
| Package manager | Go modules                                  |
| Toolchain mgr   | mise                                        |

---

## Data Sources & APIs

### 1. Goodreads RSS Feed (no auth)
```
GET https://www.goodreads.com/review/list_rss/{USER_ID}?shelf={SHELF}&page={N}
```
- Parse with `gofeed`, extract custom fields: `book_id`, `isbn`, `author_name`, `book_large_image_url`
- ISBN-10 only, often empty — supplement with Open Library
- Paginate 200 items/page until empty page returned
- Extract userId + shelf from OverReader-style or raw Goodreads URL

### 2. OverDrive Thunder API (no auth)
```
GET https://thunder.api.overdrive.com/v2/libraries/{libraryKey}/media?query={isbn or title+author}
```
- Key fields: `isAvailable`, `availableCopies`, `ownedCopies`, `holdsCount`, `estimatedWaitDays`, `covers`, `formats[].isbn`
- Search by ISBN first, fall back to `"title author"` string
- Library key = OverDrive subdomain (e.g. `mcplmd`, `lapl`)
- Cache results in SQLite for 2 hours

### 3. Open Library (no auth, free)
```
GET https://openlibrary.org/isbn/{ISBN}.json
GET https://openlibrary.org/search.json?title={T}&author={A}
```
- Fill in missing ISBN-13 and higher-res covers
- Rate limit: 3 req/sec with `User-Agent` set

### 4. Amazon PA-API 5.0
- Call REST API directly with AWS Signature V4 (no SDK needed in Go)
- `SearchItems` by ISBN → get ASIN + Kindle ASIN
- `GetItems` → Kindle price, paperback price, hardcover price, `DetailPageURL`
- Requires: Associates account, Access Key, Secret Key, PartnerTag
- PA-API 5.0 deprecated April 30 2026 — service interface designed to be swapped for Creators API
- Cache results in SQLite for 24 hours

---

## URL Input Parsing

Accept two formats:

```
# OverReader-style
https://overreader.com/overdrive/{libraries}/{source}/{userId}/shelf/{shelf}?lookfor=e

# Raw Goodreads
https://www.goodreads.com/review/list/{USER_ID}?shelf={SHELF}
```

Parse out: `libraries` (comma-separated keys), `userId`, `shelf`.

---

## Directory Structure

```
cmd/
  benreadin/
    main.go                 -- entry point, wires everything together
internal/
  db/
    db.go                   -- open DB, run migrations
    schema.sql              -- CREATE TABLE statements
  models/
    book.go                 -- Book struct
    library.go              -- LibraryResult struct
    amazon.go               -- AmazonResult struct
  services/
    goodreads.go            -- RSS fetch + pagination
    overdrive.go            -- Thunder API client
    openlibrary.go          -- ISBN-13 resolution + cover fallback
    amazon.go               -- PA-API v5 client (AWS SigV4)
    cache.go                -- SQLite TTL cache helpers
    urlparse.go             -- OverReader + Goodreads URL parser
  handlers/
    search.go               -- POST /api/search → SSE stream
    libraries.go            -- GET  /api/libraries (autocomplete)
public/
  index.html                -- search form
  results.html              -- results page (SSE consumer)
  css/
    app.css                 -- custom design system
  js/
    search.js               -- form submit, redirect to results
    results.js              -- SSE consumer, renders cards
    bookCard.js             -- book card component
.mise.toml
.env.example
go.mod
go.sum
```

---

## Database Schema

```sql
-- OverDrive availability cache (TTL 2h)
CREATE TABLE IF NOT EXISTS library_cache (
  id          INTEGER PRIMARY KEY,
  library_key TEXT NOT NULL,
  query       TEXT NOT NULL,
  result_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lib_cache ON library_cache(library_key, query);

-- Amazon pricing cache (TTL 24h)
CREATE TABLE IF NOT EXISTS amazon_cache (
  id          INTEGER PRIMARY KEY,
  isbn        TEXT NOT NULL UNIQUE,
  result_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);

-- OverDrive library directory (preloaded)
CREATE TABLE IF NOT EXISTS libraries (
  key     TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  website TEXT
);
```

TTL enforcement: `WHERE fetched_at > (unixepoch() - TTL_SECONDS)`.
Stale rows purged on startup.

---

## Search Flow

```
POST /api/search  { url, libraries: ["mcplmd","lapl"] }
  │
  ├─ parse URL → userId, shelf
  ├─ fetch Goodreads RSS pages (paginate until empty)
  │    └─ []Book{title, author, isbn10, coverUrl}
  ├─ Open Library enrichment (missing ISBN → isbn13, better cover)
  │
  ├─ fan-out: book × library  [semaphore limit=5]
  │    ├─ check library_cache (TTL 2h)
  │    └─ miss → Thunder API → cache → LibraryResult
  │
  ├─ fan-out: per book         [semaphore limit=3]
  │    ├─ check amazon_cache (TTL 24h)
  │    └─ miss → PA-API SearchItems+GetItems → cache → AmazonResult
  │
  └─ SSE stream: send one JSON event per book as results arrive
       event: book
       data: { book, libraryResults, amazonResult }
```

---

## Book Card UI

```
┌──────────────────────────────────────────────────────────┐
│  [Cover]  Title                                           │
│           Author                                          │
│                                                           │
│           MCPLMD:  ✅ Available (2 copies)                │
│           LAPL:    ⏳ 45-day wait (12 holds)              │
│           SFPL:    ❌ Not in catalog                      │
│                                                           │
│           Kindle $9.99  │  Paperback $16.99               │
│           [View on Amazon ↗]   [Send Kindle Sample]      │
└──────────────────────────────────────────────────────────┘
```

"Send Kindle Sample" links to:
`https://www.amazon.com/dp/{KINDLE_ASIN}?tag={AFFILIATE_TAG}#sampleButton`

---

## Frontend Design

Goals: clean, modern, non-sloppy. Inspired by OverReader's simplicity but polished.

- **No CSS framework** — custom design system, ~300 lines of CSS
- **Color palette:** neutral off-white background, dark slate text, accent teal/indigo
- **Typography:** system font stack (`-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif`)
- **Layout:** centered max-width container (900px), card grid (auto-fill, min 320px)
- **Cards:** white bg, subtle drop shadow, 12px radius, cover image left, content right
- **Availability badges:** pill-shaped, color-coded (green/amber/red/gray)
- **Price pills:** subtle outlined style inline
- **Search form:** single prominent URL input, multi-select library typeahead, clean CTA button
- **Results page:** progress bar while SSE is streaming, cards fade in as they arrive
- **Responsive:** single column on mobile

---

## Environment Variables

```
# Amazon PA-API (optional — app works without it, prices show as unavailable)
AMAZON_ACCESS_KEY=
AMAZON_SECRET_KEY=
AMAZON_PARTNER_TAG=        # e.g. mysite-20
AMAZON_MARKETPLACE=www.amazon.com

# Server
PORT=3000                  # customizable, defaults to 3000
DB_PATH=./data/cache.db

# Cache TTLs
CACHE_TTL_LIBRARY_SECONDS=7200    # 2 hours
CACHE_TTL_AMAZON_SECONDS=86400    # 24 hours

# Concurrency
CONCURRENCY_OVERDRIVE=5
CONCURRENCY_AMAZON=3
```

Amazon config is optional — if keys are missing, the amazon service returns empty results
and the UI shows a "prices unavailable" state gracefully.

`PORT` is read at startup; pass it via env or `.env` file to run on any port.

---

## mise Tasks

Defined in `.mise.toml` under `[tasks]`:

| Task | Command | Description |
|---|---|---|
| `mise run dev` | `go run ./cmd/benreadin` | Run dev server (loads `.env`) |
| `mise run build` | `go build -o bin/benreadin ./cmd/benreadin` | Compile binary to `bin/` |
| `mise run start` | `./bin/benreadin` | Run compiled binary |
| `mise run migrate` | `go run ./cmd/benreadin -migrate-only` | Run DB migrations and exit |
| `mise run lint` | `go vet ./...` | Vet all packages |
| `mise run test` | `go test ./...` | Run all tests |

---

## Planned Features

### 1. Goodreads Username Lookup
**Goal:** User types just their Goodreads username (e.g. `wbollock`) instead of a full shelf URL.

**How it works:**
- Add a toggle or auto-detect on the search input: if the value doesn't look like a URL, treat it as a username
- Resolve username → user ID via Goodreads (their profile URLs are `/user/show/<id>-<slug>` or can be found by scraping `goodreads.com/<username>`)
- Once user ID is resolved, construct the shelf RSS URL (`goodreads.com/review/list/<id>.rss?shelf=to-read`) as today
- Consider caching username → ID resolution in SQLite so repeat searches are fast
- UI: show a small hint like "or just enter your username" below the URL input

**Open questions:**
- Goodreads doesn't have an official username→ID API; need to scrape `goodreads.com/<username>` and parse the canonical user ID from the page
- Rate limiting / bot detection risk

---

### 2. Goodreads-Native Shelf Access (OverReader Independence)
**Goal:** Remove the dependency on OverReader URLs entirely. Users should be able to get their Goodreads shelf into BenReadin without needing to go through overreader.com first.

**Current problem:** The app was bootstrapped to accept OverReader URLs as a convenience, but this creates a hard dependency on a third-party site that could go down or change its URL format.

**Approaches (in order of preference):**

1. **Username input** (see feature #1 above) — simplest UX, just type your name
2. **Goodreads shelf RSS URL** (current) — still supported, paste the raw RSS link directly
3. **Goodreads profile URL** — paste `goodreads.com/user/show/12345` or `goodreads.com/<username>` and we resolve it
4. **Goodreads shelf page URL** — paste the full `goodreads.com/review/list/12345?shelf=to-read` page URL (not the RSS variant) and we normalize it internally
5. **Goodreads OAuth (stretch)** — if Goodreads ever re-opens API access; would allow shelf selection UI, private shelves, etc.

**What to keep from OverReader compat:**
- Parsing OverReader URLs to pre-fill libraries is still a nice convenience and can stay
- But it should be one input path among many, not the recommended one

**URL parsing logic changes:**
- `urlparse.go` already handles some cases — expand it to detect and normalize all the Goodreads URL variants above
- Show a clear error if none of the patterns match, with examples of valid inputs

---

### 3. Switchable Book Source
**Goal:** Let users pick their shelf source — not just Goodreads/OverReader URLs.

**Potential sources:**
- Goodreads (current, via RSS)
- Hardcover.app (has a public GraphQL API)
- TheStoryGraph (no public API; would need scraping or user CSV export)
- Manual ISBN list (paste a list of ISBNs or titles)

**UI approach:**
- Source selector tabs or a dropdown above the URL/username input (e.g. `Goodreads | Hardcover | StoryGraph | Manual`)
- Input label and placeholder update depending on selected source
- Each source implements the same internal `[]Book` interface so downstream processing (Libby check, Amazon prices) is unchanged

**Backend approach:**
- Abstract the current Goodreads fetcher behind a `ShelfFetcher` interface
- Add a `source` query param to `/api/search` (e.g. `source=goodreads|hardcover|manual`)
- Implement a `HardcoverFetcher` (GraphQL, needs API key or public endpoint)
- Implement a `ManualFetcher` (accept a newline-separated ISBN/title list)

---

## Conventional Commits Roadmap

```
feat: init Go module and mise toolchain
feat(db): add sqlite schema and migration runner
feat(models): add Book, LibraryResult, AmazonResult structs
feat(services): implement URL parser for OverReader and Goodreads URLs
feat(services): implement Goodreads RSS feed fetcher with pagination
feat(services): implement OverDrive Thunder API client with SQLite cache
feat(services): implement Open Library ISBN enrichment and cover fallback
feat(services): implement Amazon PA-API client with AWS SigV4 and SQLite cache
feat(handlers): add /api/search SSE endpoint and /api/libraries autocomplete
feat(ui): add search form page
feat(ui): add results page with SSE consumer and book cards
chore: add .env.example and README
```

---

## Key Design Decisions

| Decision | Reason |
|---|---|
| Go over Node/TS | Goroutines are a better fit for I/O-bound fan-out than p-limit; single binary deploy |
| modernc sqlite (pure Go) | No CGo, no gcc dep, cross-compiles cleanly |
| SSE over WebSocket | Simpler, one-directional, works through proxies, no library needed |
| SQLite cache | Survives restarts; OverDrive + Amazon are slow enough to warrant caching |
| No CSS framework | Avoid Bootstrap bloat; full control over design |
| Amazon optional | App is useful without PA-API creds; graceful degradation |
| OverReader URL compat | Users can paste existing OverReader URLs directly |
