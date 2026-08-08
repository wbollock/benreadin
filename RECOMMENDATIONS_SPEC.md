# Recommendations v2 — "Borrow This Next"

Spec for a hyper-relevant, no-AI recommendation engine. Replaces the current
single-subject Open Library approach in `internal/services/recommendations.go`.

**Status: spec — awaiting sign-off before implementation.**

## Goal

Given a Goodreads user, recommend books that are:

1. **Hyper relevant** — grounded in what they finished and what they queued,
2. **Available right now** at their selected Libby libraries,
3. **Kindle-deliverable** (`ebook-kindle` format present).

v1 hard-filters to available-now + Kindle (per decision); expanding to tiered
"short wait" results is a later toggle, and the ranking below already produces
the data for it.

**Non-goals:** no LLM/AI calls, no paid rec APIs, no Goodreads page scraping
(RSS only), no stored user accounts — same cache-only SQLite posture as the
rest of the app.

## Input signals (the taste profile)

Fetched via the existing Goodreads RSS path (`services/goodreads.go`,
`shelf_cache`), for the userID already present on the results page:

| Signal | Weight | Rationale |
|---|---|---|
| `read` shelf, rated 4–5★ | 2.0 | explicit like |
| `read` shelf, unrated | 1.0 | **finishing a book is a positive signal** — the user often doesn't rate but generally liked what they finished |
| `read` shelf, rated 1–2★ | −1 (suppress) | exclude this author/series from candidate generation |
| `to-read` shelf | 0.5 | interest signal for subjects; also an exclusion list |

Both shelves also feed the **exclusion set**: normalized, subtitle-stripped
titles (reuse `Book.SearchTitle()` + the `titlesMatch` normalization added in
the Gunslinger fix). Research confirmed naive exclusion leaks: Thunder returns
"In the Kingdom of Ice" while the shelf has "In the Kingdom of Ice: The Grand
and Terrible Polar Voyage…" — compare subtitle-stripped both ways.

## Candidate sources, in priority order

Validated 2026-07-23 against the real shelves (155 read / 297 to-read) and
freelibrary. All three work without AI.

### Source 1 — Series continuation (highest relevance)

"You finished *Dungeon Crawler Carl* #8 → *…* #9 is on the shelf now."

1. Parse Goodreads series annotations `(Series Name, #N)` from all shelf
   titles (same trailing-parenthetical convention `SearchTitle()` strips).
2. For each series with ≥1 finished book, the target is `max(read) + 1`,
   skipped if that number is already shelved anywhere.
3. Resolve the target via Thunder: search the library for the series name and
   match `detailedSeries.seriesName` (case-insensitive) +
   `detailedSeries.readingOrder == target`. Thunder exposes exact reading
   order — no guessing.
4. Drop the result if its normalized title is in the exclusion set.

Edge cases found in research, all must be handled:
- **Numbering mismatch:** Goodreads' Foundation #4 resolves to Thunder's
  "Foundation and Empire", which the user already read — step 4's title
  exclusion catches this class of bug; series math alone is not trusted.
- **Double annotations:** `(Discworld, #15; City Watch, #2)` — parse the
  *first* `Name, #N` pair only.
- **Non-integer orders:** `readingOrder` can be "3.5"; compare on the integer
  part, skip pure-novella fractions.
- **Nonexistent next book** (Earthseed #3): no Thunder match → no rec, silent.

Real output from the prototype (freelibrary): *Somewhere Beyond the Sea*
(Cerulean #2, available+Kindle), *Raising Steam* (Discworld #40,
available+Kindle), *The Fall of Hyperion*, *World Without End*, *Dune
Messiah*, … (the rest were wait-listed, so v1's hard filter would keep only
the first two — the wait-listed ones are the future "short wait" tier).

### Source 2 — Author expansion

"You finished 12 weighted-points of Terry Pratchett → here's *Hogfather*."

1. Score authors: sum of profile weights across finished books. Keep authors
   with score ≥ 2 (i.e., 2+ finished books, or one 4–5★ book), cap at ~12
   authors. Normalize author strings first — Goodreads emits `"Stephen  King"`
   (double space) and `"Kurt Vonnegut Jr."`; collapse whitespace and compare
   surname-inclusive (`strings.Contains` on last name against Thunder's
   `firstCreatorName`).
2. One Thunder search per author per library (`query=<author name>`,
   `limit=24`) — availability, Kindle format, covers, and `id` for the Libby
   deep-link all come back in the same response. No Open Library round-trip.
3. Keep items whose creator matches, title not excluded, `isAvailable`, and
   Kindle format present.
4. Rank within an author by Thunder's `ratings` / popularity; interleave
   across authors (round-robin) so one prolific author doesn't flood the list.

Prototype yield at freelibrary: **32 available-now+Kindle candidates** from 12
profile authors (Hogfather, Foundation's Edge, Ghost Soldiers, Tuf Voyaging,
The Long Walk, …) — this source alone fills the list at a big library.

### Source 3 — Subject profile (diversity backfill, phase 2)

The current engine's approach, fixed. Raw OL subjects are noise ("new york
times reviewed" and "romans, nouvelles" dominated the prototype profile and
recommended *The Da Vinci Code*). It becomes useful only with:

- An aggressive generic-subject blacklist (extend `genericSubjects`) plus
  prefix rules: drop `nyt:*`, `award:*`, `reading level*`, geographic bare
  nouns; **prefer `genre:*` and `series:*` tags** — the prototype's genuinely
  good signals were `genre:litrpg`, `genre:science fantasy`.
- Multi-subject scoring: candidate score = Σ profile-weight of shared
  subjects, require ≥2 distinct shared subjects.
- A popularity prior from OL (`sort=readinglog`) so obscure editions don't
  outrank canon.
- BISAC validation: Thunder items carry `bisacCodes`; a candidate that reaches
  the availability check but shares no BISAC top-level category with the
  profile's finished books gets demoted.

This source is **phase 2**: it needs ~40 OL calls at 3 req/s (~15 s serial)
to build the profile, so the profile must be cached (below) and computed in
the background. Sources 1+2 are Thunder-only and fast; ship them first.

## Ranking & merge

```
score = source_base            (series: 100, author: 50, subject: 10)
      + author_profile_weight  (author recs: 2 × weighted finished books)
      + subject_overlap        (phase 2)
      + availability_bonus     (luckyDayAvailableCopies > 0: small boost)
```

Round-robin interleave the top of each source rather than strictly sorting,
so the list opens with: next-in-series, then a strong author pick, etc.
Dedupe across sources by normalized title (a book can arrive via both series
and author paths — keep the higher-scored provenance for the "because" line).
Cap at `maxRecs` (raise to 15). Every rec keeps its provenance:

- `because_series`: "Next in Dungeon Crawler Carl — you finished #8"
- `because_author`: "You finished 5 books by Isaac Asimov"
- `because_subject`: "Matches your litrpg + science fantasy reading" (phase 2)

## Pipeline, endpoint, and streaming

`GET /api/recommendations` becomes an **SSE stream** (same conventions as
`/api/search`: padding comment, named events, `WriteTimeout: 0` already set).

```
event: rec_progress   {"stage":"profile"|"series"|"authors","done":n,"total":m}
event: rec            {Recommendation}          — emitted as each one confirms
event: recs_done      {"count":n}
```

Server flow per request (all bounded by existing patterns):

1. Fetch `read` + `to-read` shelves (shelf_cache, singleflight — 2 RSS pages
   + 3 RSS pages for this user's shelf sizes).
2. Build profile + exclusion set in memory (pure computation).
3. Series resolution fan-out: ≤ ~20 Thunder searches under a
   `semaphore.Weighted(CONCURRENCY_OVERDRIVE)`.
4. Author expansion fan-out: ≤ 12 Thunder searches per library, same semaphore.
5. Stream each confirmed rec immediately; frontend appends cards.

Cold-path estimate: ~30–45 Thunder calls ≈ 5–10 s to first rec, well under
the 15–60 s ceiling discussed; warm path is instant via caches.

## Caching (new + reused)

| Cache | Key | TTL | Notes |
|---|---|---|---|
| `shelf_cache` (existing) | `userID\|shelf` | 5 min | reused for read + to-read |
| `library_cache` (existing) | `libraryKey\|query` | 2 h | Thunder author/series searches go through the same table — new helper reuses `SetLibrary`-style rows keyed by the search query |
| `rec_profile` (new) | `userID` | 24 h | serialized profile: author weights, series progress, exclusion set, (phase 2) subject weights. Tastes change slowly; this makes repeat visits instant and is what makes phase 2's OL cost acceptable |

Schema addition in `schema.sql` (idempotent, per convention):

```sql
CREATE TABLE IF NOT EXISTS rec_profile (
  user_id    TEXT PRIMARY KEY,
  profile    TEXT NOT NULL,   -- JSON
  fetched_at TIMESTAMP NOT NULL
);
```

## Frontend

Results page keeps the existing recommendations section, upgraded:

- Cards stream in via the new SSE endpoint after the main search's `done`
  event (don't compete with availability checks for Thunder connections).
- Each card: cover, title/author, **"Available now · Kindle"** badge, the
  "because" provenance line, and the same Libby deep-link as search results
  (built from Thunder `id` — benefits from the Gunslinger fix's verified IDs).
- Empty state: "No available-now Kindle matches at your libraries" — expected
  at small libraries under the v1 hard filter; this is where the future
  tiered mode plugs in.

## Config

```
REC_MAX=15                  # max recommendations
REC_MAX_AUTHORS=12          # author-expansion breadth
REC_PROFILE_TTL=24h
```

Concurrency rides the existing `CONCURRENCY_OVERDRIVE` semaphore.

## Metrics

`recs_generated_total{source=series|author|subject}`,
`rec_profile_cache_hits_total`, plus the existing
`upstream_errors_total{service=overdrive}` covers Thunder failures.

## Testing

- Unit: series-annotation parser (double annotation, `#1-3` ranges, no
  annotation), profile weighting (unrated-finished = positive, 1–2★
  suppression), exclusion-set normalization (subtitle leak case), interleave
  + dedupe.
- Fixture-driven: golden Thunder JSON for the series/author flows (no live
  calls in CI).
- E2E via `.claude/skills/verify`: real shelf + freelibrary, assert the
  stream yields ≥1 series rec and ≥1 author rec, none of which appear on
  either shelf.

## Phasing

1. **Phase 1 (ship first):** profile + exclusion set, series continuation,
   author expansion, SSE endpoint, frontend cards, `rec_profile` cache.
   Thunder-only — no new external dependencies.
2. **Phase 2:** subject profile with the blacklist/`genre:` preference, BISAC
   validation, OL popularity prior — backfill/diversity only.
3. **Later (explicitly deferred):** tiered availability ("short wait ~4d"
   badges), general non-Kindle recs, filter chips.

## Appendix — real prototype output (freelibrary, this user, 2026-07-23)

Series continuation (v1 filter keeps ★ rows):
```
★ Cerulean Chronicles #2: Somewhere Beyond the Sea   AVAILABLE+KINDLE
★ Discworld #40:          Raising Steam              AVAILABLE+KINDLE
  Kingsbridge #2:         World Without End          wait ~4d
  Hyperion Cantos #2:     The Fall of Hyperion       wait ~28d
  Dune #2:                Dune Messiah               wait ~82d
```

Author expansion (all available-now + Kindle; 32 total, sample):
```
Hogfather / Raising Steam / Dodger        — Terry Pratchett   (12w finished)
Foundation's Edge / Forward the Foundation — Isaac Asimov      (7w finished)
Ghost Soldiers                             — Hampton Sides     (6w finished)
Tuf Voyaging / Dying of the Light          — George R.R. Martin (5w finished)
The Long Walk / Later / If It Bleeds       — Stephen King      (4w finished)
```

Subject profile as-is (why it's phase 2 and needs the blacklist): top raw
profile subjects were "new york times reviewed" (13.0) and "romans,
nouvelles"; top unfiltered recs included *The Da Vinci Code*. The salvageable
signals were `genre:litrpg` and `genre:science fantasy`, which correctly
surfaced Dungeon Crawler Carl titles.
