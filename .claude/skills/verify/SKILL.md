---
name: verify
description: Build, run, and drive benreadin locally to verify changes end-to-end (SSE stream via curl, UI via Playwright Firefox).
---

# Verifying benreadin

## Build & launch

```bash
go build -o /tmp/benreadin ./cmd/benreadin
PORT=3999 DB_PATH=/tmp/benreadin-cache.db /tmp/benreadin > /tmp/benreadin.log 2>&1 &
curl -s localhost:3999/api/health   # {"status":"ok",...}
```

`.env` in the repo root is empty → Amazon PA-API is disabled locally
(`"amazon":false` in health). Gutenberg catalog loads in the background at
startup and can slow the first search by ~10s — warm up with one throwaway
search before timing anything.

## Drive the SSE surface (the core flow)

A real shelf + real OverDrive library keys work unauthenticated:

```bash
curl -sN 'localhost:3999/api/search?url=97106512-william&libraries=nypl&libraries=lapl'
```

Event order to expect: padding comment, `progress`, `book_stubs` (all
placeholders at once), per-book `book` + `progress`, `availability_done`
(only when there were cache misses), `book_update` patches (enrichment:
page count / ISBN / prices), `done`. Fully-cached runs skip
`availability_done` and `book_update`. Server book cache TTL is 2h;
`&refresh=true` bypasses it. Pipe through a timestamp loop to measure
phases. The enrichment tail for ~300 cold books takes 1–3 min (OpenLibrary
is capped at 6 concurrent) — use a generous curl timeout or the cache
converges over repeated runs.

## Drive the UI (Playwright)

Headless **Chromium doesn't launch** on this host (missing `libgbm`, no
sudo). **Firefox works** with one extracted lib:

```bash
pip3 install --user playwright && python3 -m playwright install firefox
cd /tmp && apt-get download libasound2 && dpkg -x libasound2*.deb extracted
LD_LIBRARY_PATH=/tmp/extracted/usr/lib/x86_64-linux-gnu python3 <script>
```

In the script use `p.firefox.launch()` and
`browser.new_page(bypass_csp=True)` — the app's CSP otherwise blocks
Playwright's `evaluate`/`wait_for_function`.

Results page: `/results.html?url=<shelf>&libraries=nypl&libraries=lapl`.
Useful hooks: `#progress-label` text ("filling in details…" during
enrichment, "Done — checked N books" at the end), `.book-card` /
`.book-card--stub` / `.hidden` classes, `#sort-select`, `#shuffle-btn`,
`.filter-chip[data-filter=...]`, `#refresh-btn` (forces the full
pipeline).

## Gotchas

- System node is v12 — `node --check` fails on `??` in the frontend JS;
  parse with `npx acorn` instead.
- `go vet`/gopls diagnostics may complain about `go 1.25.0` in go.mod —
  stale toolchain noise, `go build ./...` is the truth.
