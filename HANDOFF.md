# Hand-off: Recommendations v2 polish pass

Uncommitted working-tree state as of 2026-07-24. Nothing in this branch has
been committed yet — `git status` shows all changes below as modified/new
files, ready to review together and commit in one or more logical chunks.

## Context

Two threads of work in this session:

1. **Bug fix (done, verified)**: `The Gunslinger (The Dark Tower, #1)` linked
   to the wrong Libby title (a series tie-in, "Charlie the Choo-Choo").
   Root cause: OverDrive/Thunder text search included the Goodreads series
   annotation in the query and trusted the top hit unconditionally. Fixed in
   `internal/models/book.go` (`SearchTitle()` strips the annotation) and
   `internal/services/overdrive.go` (`titlesMatch` verifies the result before
   linking; only ISBN queries trust position 0). Covered by
   `overdrive_test.go` / `book_test.go`. This part is solid and fully tested.

2. **Recommendations v2 (mostly done, one edit in flight)**: built per
   `RECOMMENDATIONS_SPEC.md` — a no-AI engine that recommends the next unread
   book in series you're reading, plus more from authors you've finished,
   filtered to available-now + Kindle at your libraries. Then iterated twice
   based on live feedback (see below).

## What's done and verified

- **Backend engine** (`internal/services/recprofile.go`,
  `internal/services/recommendations.go`): taste profile from `read` +
  `to-read` shelves (unrated-but-finished counts as a positive signal,
  1-2★ suppresses that author/series), series-continuation source (Thunder
  `detailedSeries.readingOrder`), author-expansion source (round-robin
  interleaved). Cached in new `rec_profile` table (24h TTL,
  `internal/db/schema.sql` + `cache.go`). Streamed via new SSE endpoint
  `GET /api/recommendations` (`rec_progress`/`rec`/`recs_done` events),
  same conventions as `/api/search`.
- **Full enrichment parity** (this session's first follow-up): rec cards now
  run through the *same* enrichment pipeline as normal search results —
  Open Library ISBN/page-count backfill, Gutenberg free-book match, Amazon
  pricing — and are emitted as `models.Recommendation{Book, LibraryResults,
  AmazonResult, GutenbergResult, Source, Because*}`, i.e. the same shape as
  `models.BookEvent` plus provenance fields. Enrichment is deferred to the
  moment a candidate is actually chosen for emission (not run speculatively
  on every candidate), bounding API calls to ~`maxRecs`.
- **Frontend parity**: `buildBookCard()` (`public/js/bookCard.js`) grew an
  optional third `extra` param (HTML inserted after the author line) so rec
  cards render with the *exact* same component as search results — cover,
  description, page count, full per-library status badges (not just
  "available"), price pills, Borrow/Kindle buttons — plus a `.rec-because`
  provenance line. `results.js`'s rec click handler now streams `buildBookCard`
  output straight into `#recs-grid` (which shares the `.book-grid` class).
- **UX fixes from live user feedback** (second follow-up, this turn):
  - Button renamed "Suggest similar" → "Recommend me more books ↓";
    clicking now `scrollIntoView`s the recs panel immediately and applies a
    brief `.recs-panel--highlight` glow, so it's obvious where the content
    landed.
  - Fixed a real CSS bug in `.recs-header`: it used `align-items: baseline`
    plus a `margin-top: -5px` hack on the subtitle, unlike the main
    `.results-header`'s clean `center` + `space-between`. Rewritten to match.
  - **Root cause found for "no book images"**: recommendation covers come
    directly from OverDrive's CDN (`img1/2/3.od-cdn.com`), but the CSP
    (`internal/middleware/security.go`) only whitelisted
    `i.gr-assets.com` / `images-na.ssl-images-amazon.com` /
    `covers.openlibrary.org` — every OverDrive cover was silently blocked by
    the browser (confirmed by pulling live `cover_url` values from the SSE
    stream: 15/15 were `od-cdn.com`, none were a whitelisted domain). **Fixed**:
    added `https://*.od-cdn.com` to `img-src`. Not yet re-verified in a
    browser screenshot — do that first when resuming.
  - **Ratings decision made with the user**: no free/legal API exists for
    real Goodreads ratings on non-shelf books (RSS is shelf-only, and
    scraping individual Goodreads book pages was explicitly flagged as a
    ToS-gray non-goal in the original spec). User chose **Open Library's
    community rating**, honestly labeled (not presented as "Goodreads").
    Implemented: `models.Book` gained `RatingsCount` + `RatingSource` fields;
    `OpenLibraryService.FetchRating()` (new, in `openlibrary.go`) best-effort
    backfills `AverageRating`/`RatingsCount` from Open Library's
    `search.json?fields=ratings_average,ratings_count` **only when
    `AverageRating` is currently 0** (never overwrites a real Goodreads
    rating) — called from `recommendations.go`'s `enrich()` right after the
    existing `Enrich()` call. Frontend (`bookCard.js`) renders an `OL·<count>`
    tag next to the star and an honest tooltip
    ("Open Library community rating (not Goodreads)") when
    `rating_source === 'openlibrary'`. CSS tag style added
    (`.rating-source-tag` in `app.css`).

## In flight / not yet done

I was mid-edit on `public/results.html` (adding a "back to top" button and a
sort control for the recs grid) when interrupted — **no HTML/JS was written
for these two items yet**, only planned. Specifically still to do:

1. **Back-to-top button** (user: "maybe needs some way to return to top
   quickly too"). Plan: a small fixed-position button (bottom-right,
   `.back-to-top` class, styled off the existing `.btn-secondary` pattern),
   hidden until `window.scrollY` passes a threshold (~400px), smooth-scrolls
   to top on click. Needs: HTML element near the end of `<body>` in
   `results.html` (around line 180, before the closing scripts), CSS in
   `app.css`, and a scroll listener + click handler in `results.js`.

2. **Sorting for the recommendations grid** (user: "have sorting"). Plan:
   mirror the existing main-results sort pattern (`#sort-select` /
   `applyView()` in `results.js`) — add a `#recs-sort-select` in the
   `.recs-header` area of `results.html`, buffer streamed recs into a
   `let allRecs = []` array (`{rec, el}` pairs, parallel to the existing
   `allBooks`/`bookElements` pattern), and an `applyRecsView()` that
   re-appends the existing DOM elements in sorted order (no rebuild). Likely
   sort options: Recommended order (default/as-streamed), Highest rated (now
   meaningful thanks to the OL rating work above), Title A→Z, Most copies
   available. Call `applyRecsView()` both on each new `rec` SSE event (live
   re-sort while streaming) and on `#recs-sort-select` change.

3. **Re-verify the cover-image fix in a browser.** The CSP change is made
   but I have not re-run the Playwright/Firefox verification (see
   `.claude/skills/verify`) to confirm covers actually render now, or taken
   a fresh screenshot to check whether "the css looks slightly off" is fully
   resolved post-image-fix (my working theory was that broken image tiles
   were the dominant cause of the perceived misalignment, but that's
   unconfirmed — look again once real covers are loading).

4. **Build/vet/test** have NOT been re-run since the CSP, ratings-field, and
   `FetchRating` changes in this turn. Run `go build ./...`, `go vet ./...`,
   `go test ./...` before doing anything else on resume — the Go side has
   compiled cleanly at every checkpoint so far but hasn't been checked since
   the last batch of edits.

5. No new Go unit tests were added for `FetchRating` (network-calling,
   consistent with the rest of `openlibrary.go` having no existing tests).
   If a pure-logic guard test is wanted (e.g. "never overwrites a nonzero
   AverageRating"), it'd need a small refactor to extract the guard as a
   testable function, or an httptest-based test — neither exists yet.

## How to resume

Pick up at step 3 above (re-verify images), then 1 and 2 (back-to-top,
sorting) in either order, then step 4 (final full check). The
`.claude/skills/verify` skill has the build/launch/Playwright recipe already
used twice this session — same shelf (`97106512-william`) and library
(`freelibrary`) work well as a live test case and are already warm in the
prior conversation's context.

Nothing has been committed. When done, a commit message should probably
split into at least two logical commits (the Gunslinger/OverDrive matching
fix vs. the recommendations feature + its follow-up polish) — confirm with
the user before committing, per standing instructions.
