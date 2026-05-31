# BenReadin Polish & Production-Ready Plan

Audience: an LLM (or human) implementer who has not seen prior conversation context. Goal: take BenReadin from "works on my machine" to a polished thing the author can show off to real users — fast, responsive, error-free, and visually distinctive without the visual tells of a generic AI-generated app.

The current state is a Go server (`cmd/benreadin/main.go`, chi router, SSE streaming) serving a vanilla-JS frontend (`public/index.html`, `public/results.html`, `public/js/*.js`, `public/css/app.css`). Books are fetched from Goodreads RSS, cross-checked against the OverDrive Thunder API for Libby availability, optionally enriched with Open Library, Project Gutenberg, and Amazon PA-API. Results stream to the browser as SSE events. Caching is SQLite (`internal/db/schema.sql`).

The plan is divided into independent, ordered phases. Each phase lists files to touch, the exact problem, the fix, and a verification step. Phases later in the list depend on the earlier ones being merged, but within a phase, tasks are independent unless noted. Implement phases 1–4 before any others; they unblock everything else.

---

## Phase 0 — Ground rules (read before editing)

1. **No new dependencies unless explicitly listed in this plan.** The frontend is plain JS by design; do not add React/Vue/Svelte/etc. The Go backend is intentionally small.
2. **No new emojis in UI copy.** The current UI is littered with sparkles (`✨`), lightning bolts (`⚡K`), books (`📚 📖`), checkmarks (`✓`), hourglasses (`⌛`), arrows (`↗`), moon (`🌙`). These are AI-app cliches. Replace with SVG icons or pure text. See Phase 2.
3. **No "vibe" copy.** Specifically forbidden phrases anywhere in the UI: "✨ Recommendations", "You Might Also Like", "Find recommendations based on your shelf", "Similar to ...", "Finding recommendations…". Rewrite per Phase 2.
4. **Inline styles in HTML are banned.** Move every `style="…"` attribute in `public/index.html` and `public/results.html` to `app.css`. Same for inline `<script>` blocks except the theme bootstrap (which must stay inline to avoid FOUC).
5. **Run `gofmt ./...` and `go vet ./...` before committing.** No lint suppressions.
6. **Test against William's shelf (`97106512-william`) and an empty/private shelf.** Both must render meaningful UI.
7. **Browser-test in Chrome and Safari before declaring a phase done.** Safari is stricter about SSE buffering and CSS `backdrop-filter`.

---

## Phase 1 — Performance: stop re-rendering everything on every event

**Problem.** `public/js/results.js` calls `renderGrid()` (line ~166) which does `bookGrid.innerHTML = books.map(...)`. This rebuilds the entire DOM on every sort change *and* on the final `done` event. For a 200-book shelf, that is ~200 cards × dozens of nodes each blown away and rebuilt every interaction. Worse: on each incoming `book` SSE event, the code already does a per-card stub-replacement (line ~431), and then `renderGrid()` is called again on `done` (line ~471), which throws away all the carefully-inserted cards and re-parses them.

### 1.1 Replace `innerHTML` rebuilds with DOM updates

- Stop calling `renderGrid()` on the `done` event. Instead, on `done`:
  - Compute the desired sort order for the cards already in the DOM.
  - Reorder them with `bookGrid.append(...sortedCards)` (a single reflow; `append` of already-attached nodes just moves them).
- On sort/filter change:
  - Reorder existing cards in place via `append(...sortedNodes)` — no `innerHTML`.
  - Toggle a `.hidden` class for filtered-out cards.
- Maintain a parallel `Map<goodreadsId, HTMLElement>` so lookup is O(1) (currently `bookGrid.querySelector` per book — O(n²) at scale).

### 1.2 Use `DocumentFragment` for the initial stub batch

In `startStream()` → `book_stubs` listener: build the stub HTML in a `<template>` element or via `DocumentFragment`, then append once. Stop using `insertAdjacentHTML` for hundreds of children — measure with `performance.mark` before/after.

### 1.3 Debounce the `progress` event renders

The server sends a `progress` event after every cache hit and after every completed book. The label updates the text node every time. Wrap with `requestAnimationFrame` so updates coalesce.

### 1.4 Lazy-load cover images that are below the fold

Covers already have `loading="lazy"`, but the cards are all in the initial DOM after `book_stubs`. Add `decoding="async"` on every `<img>` and add `width`/`height` attributes (76×114 for main cards, 54×81 for recs) so the browser reserves layout — no CLS, no layout thrash as images load.

### 1.5 Parallelize Goodreads RSS pagination

`internal/services/goodreads.go` `FetchShelf` walks pages 1, 2, 3, … sequentially. For a 1000-book shelf that's 5 round-trips of ~1s each = 5s before any availability check starts.

- Fetch page 1 first to confirm the shelf is non-empty and to detect whether pagination is needed (fewer than 200 items means done).
- If page 1 is full, kick off pages 2..N in parallel with `errgroup.Group` and a small concurrency cap (4). Stop when a page returns < 200 items (or page 5, whichever first — cap at 1000 books).
- Preserve shelf order: collect results into a `[]grItem` indexed by page, then concatenate.

Verification: `time curl -N 'http://localhost:3000/api/search?url=…'` against a large shelf, compare `Fetching your shelf…` → `Found N books` wall-clock before/after.

### 1.6 Stop double-searching Amazon

`internal/services/amazon.go` `searchByISBN` makes TWO PA-API search calls per book (one for `SearchIndex: "Books"`, one for `SearchIndex: "KindleStore"`). With `CONCURRENCY_AMAZON=3` and 200 books, that's 400 signed requests. Combine into a single call: PA-API supports `SearchIndex: "All"` with a Resource list including the format. If that doesn't work for Kindle ASIN extraction, accept that books may not have a Kindle ASIN — the UI already falls back to a search URL. Document the tradeoff in the function comment.

---

## Phase 2 — Visual polish: remove AI-slop telltales

**Problem.** The current visual identity reads as "generated by an LLM": teal accent + rounded gradient hero + emoji-as-icon + lightning bolt in the logo + sparkle on the recs button + "✨ Recommendations" copy. Replace with a coherent, restrained, library/print-shop-inspired identity.

### 2.1 Replace the logo

- Current logo: complex SVG of a face with bifocals and a lightning bolt (`public/index.html` lines 27–44, duplicated in `public/results.html`). Visually busy, reads as cartoon AI mascot.
- Replace with a wordmark: "benreadin" set in a real typeface (Fraunces, Source Serif Pro, or IBM Plex Serif — pick Fraunces and self-host) with a single, restrained glyph to its left. The glyph should be either:
  - a 2-line book spine icon (rectangle + horizontal rule) at 20×20, stroke 1.5, no fill, no gradient; or
  - just the wordmark with no icon at all.
- The favicon needs to be a real `.ico` and PNG (`/public/favicon.ico`, `/public/icon-192.png`, `/public/icon-512.png`, plus `/public/apple-touch-icon.png` 180×180). Generate via a single SVG source committed at `/public/icon.svg`. Reference all of them in `<head>` per [evilmartians/favicon-cheatsheet](https://evilmartians.com/chronicles/how-to-favicon-in-2021-six-files-that-fit-most-needs).
- Remove the inline `style="background:none;padding:0;line-height:0;border-radius:0;"` overrides — that's a smell that the logo container styling is wrong.

### 2.2 Color palette overhaul

Replace the teal-everywhere palette (`--accent: #116d63`). Use a two-color system:
- **Ink** `#1c1d1f` for primary text and the logo (replaces `--text`).
- **Paper** `#f7f4ee` for the page background — warm off-white, evokes book paper (replaces `--bg: #eef3f9` which is a Slack-blue tint).
- **Surface** `#ffffff` for cards.
- **Accent** a single deep red `#a23636` (book ribbon / library binding red), used sparingly: primary button, focus rings, link hover only.
- Status colors stay semantic but desaturate:
  - Available: `#2e7d4a` on `#e6f1e8` (forest green on parchment).
  - Wait: `#9c6a1d` on `#f4ecd8` (amber/sepia).
  - Not found: `#6b6b6b` on `#ececec`.

Implement as new CSS custom properties at the top of `app.css`. Dark mode: dark warm `#1a1814` paper, `#252321` surface, accent `#c46a6a`. Test contrast (WCAG AA minimum 4.5:1 for body, 3:1 for large text) with the Chrome DevTools contrast checker.

### 2.3 Typography

- Body: `"Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif` at 15px / 1.55 line-height. Self-host Inter (variable font, 1 woff2 file, ~120KB) under `/public/fonts/inter.woff2` and serve with `Cache-Control: public, max-age=31536000, immutable`.
- Display (hero, results headings): `"Fraunces"`, variable font, self-hosted at `/public/fonts/fraunces.woff2`.
- Mono: keep system mono stack (no self-host needed).
- Remove `"Avenir Next"` from the font stack — it's a paid Apple font most users don't have, and the fallback chain looks accidental.
- Add `<link rel="preload" as="font" type="font/woff2" crossorigin>` for both fonts in both HTML files.

### 2.4 Replace the gradient hero

The current hero (`app.css` lines 170–186) is a dark teal gradient with two radial blobs — a 2023 AI-app trope. Replace with:
- Flat paper background that matches the rest of the page.
- A single horizontal rule (1px ink, 20% opacity) under the H1 to anchor the layout.
- H1 in Fraunces, ink color, no gradient text fill.
- The hero is dark and the rest of the page is light, which is jarring on scroll. Either make the entire page light, or commit fully to dark hero + light page with a clear visual break (an ink-on-paper border at the bottom of the hero). Pick the former — it reads more like an editorial site, less like a SaaS landing page.

### 2.5 Strip all emoji from the chrome

Inventory of emoji in source — remove or replace each:
- `🌙` theme toggle button (both HTML files) → SVG sun/moon (Lucide icons, MIT, copy the SVG paths in).
- `↗` external-link arrow (multiple buttons in `bookCard.js`) → CSS `::after` content with an SVG background, or just drop entirely — modern users know external links underline.
- `✨` recommendations button (`results.html` line 83) → drop, rename button to "Suggest similar".
- `📚` cover placeholder (`bookCard.js` line 128, 202, 573) → replace with a styled `<div>` that has the book title's first letter set in Fraunces, ink-on-paper, like a library card.
- `📖` page count (`bookCard.js` line 135) → drop, just show `"320 pages"`.
- `⚡K` kindle badge → replace with a small `K` glyph in a 14×14 amber square, no lightning.
- `★ / ☆` rating glyphs (`bookCard.js` lines 142–143) → keep the Unicode stars (they read as typography, not emoji) but in the ink color, not orange. Title-cased "★★★★☆" is fine.
- `⌛` hourglass in wait badge → drop. The text "12-day wait" is clear.
- `✓` checkmark on available → drop. The badge color is enough.
- `&#9998;` pencil on chip-edit → SVG pencil icon.
- `&times;` close on chip-remove → SVG X icon.
- Inline `⚡` favicon SVG in both HTML `<head>` blocks → replace per 2.1.

### 2.6 Tone down shadows, borders, and rounded corners

Current CSS has 5 shadow tokens (`--shadow-xs` through `--shadow-xl`) and 4 radius tokens, used liberally. Generic "soft modern" SaaS look. Tighten:
- Use exactly two shadow values total in the entire app: a flat 1px border (no shadow) for cards by default, and a 0 2px 6px shadow on focused/hovered cards.
- Use exactly two radius values: 4px (small things) and 8px (cards). Drop `--radius-lg: 18px` and `--radius-xl: 24px`. Pills become 4px, not 100px (less "designed").
- No gradients anywhere except inside book covers (and even there: prefer the title-letter placeholder).

### 2.7 Rewrite all UI copy

Specific replacements (search the files for each phrase, replace exactly):
- Page title: "BenReadin — Check your Goodreads shelf on Libby" → "benreadin — your to-read list, but borrowable".
- H1: "Find your <span>to-read</span> books<br>available on Libby" → "Your to-read pile,<br>checked out of the library." (no gradient span).
- Subhead: "Paste your Goodreads shelf URL and BenReadin checks every book…" → "Paste a Goodreads shelf. We'll tell you what's borrowable on Libby right now, what's on hold, and what's free in the public domain."
- Submit button: "Search Shelf" → "Check availability".
- Recs button text on click: "Finding recommendations…" → "Finding similar titles…"; idle: "Suggest similar".
- Recs panel title: "You Might Also Like" → "More from your libraries".
- Recs subtitle: "Books similar to your shelf available to borrow right now on Libby." → "Available now in the libraries you picked, drawn from subjects on your shelf."
- Recs "Similar to <em>{title}</em>" → "Picked from your shelf's {subject}" (requires backend change: include the subject string in the `Recommendation` model — see Phase 4.5).
- Status area "Starting search..." → "Starting…".
- Done message "Done — checked {n} books" → "{n} books checked." Drop the dash.
- Footer 3-paragraph disclaimer → collapse to one line: "benreadin is not affiliated with Goodreads, Libby, OverDrive, or Amazon. Borrow data is live from OverDrive. As an Amazon Associate, benreadin may earn from qualifying purchases." Single paragraph, smaller.
- "↺ Refresh" → "Refresh".
- "← New search" → "Start over".

### 2.8 Form & input visuals

- Inputs currently have `border-radius: 12px` and 1.5px borders with a 4px focus ring. That's a SaaS form. Change to: 4px radius, 1px ink border at 20% opacity, focus ring 2px solid accent (the new red), no glow shadow.
- The library chip pills are very pill-shaped. Square them off to 4px to match.
- Drop the URL-example button (the "William's to-read shelf" demo). Replace with a small `<p class="hint">` below the input: `Tip: try <a>97106512-william</a>` — a plain link, not a button-pill.

---

## Phase 3 — UX: reduce overwhelming choices, surface what matters

### 3.1 Shrink the sort menu

`results.html` lines 117–134: 15 sort options. Users won't read this. Cut to 5:
- "Available first" (default; rename from "Available first" — already exists).
- "Shortest wait".
- "Highest rated".
- "Shelf order".
- "Title A→Z".

Move everything else behind a "More" disclosure or just delete (`default_desc`, `unavailable_first`, `pages_*`, `wait_desc`, `user_rating_*`, `rating_asc`, `kindle_first` can all go).

### 3.2 Shrink filters

Six filter chips + an AND/OR mode toggle is too many decisions. Cut to:
- All
- Available now
- On hold
- Not on Libby

Keep "Kindle delivery" and "Free (Gutenberg)" as compact toggle pills off to the side, not in the same chip row — they're augmentations, not primary filters. Drop the AND/OR mode toggle entirely (users won't use it; default to OR).

### 3.3 Library rename interaction is too aggressive

Currently in `results.js` line ~542, any click anywhere on a `.lib-label` opens the rename modal. Too easy to trigger accidentally on mobile. Fix:
- Make the `.lib-label` `<button>` instead of a `<span>` so it has explicit affordance and keyboard support.
- Show a faint pencil icon next to the label on hover only.
- Hide rename behind a right-click context menu OR a long-press OR a small "Rename libraries" link in the results header that toggles "edit mode" — only in edit mode do labels become clickable. Pick option 3 (edit mode toggle) — least surprising.

### 3.4 Empty / error states need illustrations and language

- **Shelf returned zero books** (private shelf is the most likely cause). Currently: `sendEvent("done", {total:0, message:"No books found on shelf"})`. The frontend just shows "Done" — confusing. Replace with a styled empty-state panel that explains: "Goodreads returned no books. The shelf may be private — open Goodreads → Settings → Privacy → 'Who can see my profile' set to Anyone."
- **All books "Not found" on Libby**: warn that the library key may be misspelled. Show a one-click action "Search OverReader for these libraries" linking to overreader.com so users have a fallback.
- **Network error mid-stream**: currently a red banner. Add a "Retry" button that calls `startStream(true)`.
- **404 on shortlink**: currently shows the default chi 404. Add a friendly `404.html` reachable via the static handler. Wire chi to fall back to `404.html` instead of the default text response when the path doesn't match.

### 3.5 First-time landing page needs a one-screen explanation

Most visitors will not know:
1. They need to make their Goodreads profile public.
2. What a Libby/OverDrive library key is and where to find it.

Add a small "How it works" section below the search card on `index.html` — three steps with tiny illustrations or numbered headings, no marketing copy. Stay restrained. Mark it `id="how"` and link to `#how` from the form's helper text ("Don't know your library key? See How it works.").

### 3.6 Mobile improvements

- The hero text is currently 2.1rem on `<700px`. On a 360px iPhone SE that wraps awkwardly. Drop to 1.85rem and tighten letter-spacing to -0.5px.
- The library chip input area can become too tall on mobile when several chips are selected. Cap visible chips at 3 + "+N more" overflow.
- The filter chip horizontal scroll has a gradient mask hint (`app.css` lines 1234–1236) — replace with snap-points + visible scroll affordance text "scroll →" rather than a fade-out (the fade looks like a CSS bug to users).
- Tap targets: many small buttons (chip-edit, chip-remove, lib-label) are < 32px. Bump to minimum 36×36 on touch screens via `@media (pointer: coarse)`.

### 3.7 Keyboard shortcuts

Add lightweight keyboard support on the results page:
- `/` focuses sort select.
- `1`–`4` toggle filter chips by order.
- `?` opens a small shortcuts cheat-sheet modal.
- `g h` (vim-style two-key) goes back to "New search".
- Implement via a single `keydown` listener on `document`, no library. Skip when focus is in an `<input>`/`<select>`/`<textarea>`.

### 3.8 Share button (Web Share API on mobile)

When the shortlink resolves, on mobile (`navigator.share` available), the "Copy link" button should call `navigator.share({title, url})` instead of clipboard. Fall back to clipboard on desktop.

### 3.9 OG/Twitter meta tags

Add to `<head>` of both HTML files:
```html
<meta property="og:title" content="…">
<meta property="og:description" content="…">
<meta property="og:image" content="/og.png"> <!-- 1200×630 -->
<meta property="og:type" content="website">
<meta name="twitter:card" content="summary_large_image">
```
Generate `og.png` once and commit it under `/public/`. A simple ink-on-paper composition with the wordmark and tagline.

---

## Phase 4 — Backend reliability & speed

### 4.1 HTTP response compression

There is no gzip middleware. The Goodreads-shelf SSE stream pushes a lot of JSON; covers and JS are uncompressed. Add `github.com/go-chi/chi/v5/middleware.Compress(5)` to `cmd/benreadin/main.go` and exclude it from `Content-Type: text/event-stream` (SSE must not be compressed by a buffering proxy — set `Cache-Control: no-transform` on SSE responses). Verify with `curl -I -H 'Accept-Encoding: gzip' http://localhost:3000/css/app.css`.

### 4.2 Cache-Control on static assets

The bare `http.FileServer(http.Dir("public"))` sets no Cache-Control. Every reload re-downloads CSS/JS. Wrap it to add:
- `Cache-Control: public, max-age=31536000, immutable` for any file with a hash in the name (introduce a tiny asset-hash step — see 4.3).
- `Cache-Control: no-cache` for `*.html`.
- `Cache-Control: public, max-age=3600` for fonts and images.

### 4.3 Asset cache-busting (light touch — no bundler)

Don't introduce a bundler. Do this:
- On server start, compute a short hash of each `public/css/*.css` and `public/js/*.js` file (sha256, first 8 hex chars).
- Serve them at `/css/app.<hash>.css` and rewrite references in HTML on-the-fly using a tiny `html/template`-driven handler that replaces `{{cssHash}}` / `{{jsHash}}` placeholders. Or, simpler: serve the HTML files through a `html/template` that injects hashed paths from a `map[string]string{"app.css": "<hash>", ...}`.
- This lets you cache CSS/JS forever without stale-cache user pain.

### 4.4 Security headers

Add a middleware that sets, for every response:
- `Content-Security-Policy: default-src 'self'; img-src 'self' https://images-na.ssl-images-amazon.com https://i.gr-assets.com https://covers.openlibrary.org data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; frame-ancestors 'none'`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy: geolocation=(), camera=(), microphone=()`
- `Strict-Transport-Security: max-age=31536000; includeSubDomains` (only when `X-Forwarded-Proto: https` or `cfg.TLS == true` — don't break local dev).

Test CSP carefully — `'unsafe-inline'` for style is needed because of the theme bootstrap inline `<script>` and the existing inline `style=""` attributes. After Phase 2.0 (no inline styles), tighten to no `'unsafe-inline'` for style. The theme bootstrap can use a `nonce` attribute (Go middleware injects a per-request nonce, template substitutes it into both the `<script nonce="…">` tag and CSP header).

### 4.5 Recommendation: include subject in the response

Per 2.7 ("Picked from your shelf's {subject}"), modify:
- `internal/models/recommendation.go` (if it doesn't already): add `BecauseSubject string` field.
- `internal/services/recommendations.go` `fetchSimilar`: capture the chosen subject and put it on each `recCandidate`. Plumb through to `Recommendation.BecauseSubject`.
- `public/js/results.js` `renderRecs`: use `rec.because_subject` if present.

### 4.6 Retry once on transient OverDrive failures

`internal/services/overdrive.go` `fetchThunder`: on 5xx or `net.Error` `Timeout()`, retry once with a 250ms backoff. Don't retry on 4xx (library key doesn't exist).

### 4.7 Goodreads private-profile detection

Goodreads RSS returns 200 with empty `<channel><item>` when the profile is private — indistinguishable from an empty shelf. Add a heuristic: if shelf returns 0 books AND `len(books)==0`, also try fetching the profile HTML at `https://www.goodreads.com/user/show/{userid}`. If that returns a `noindex` meta or a "this profile is private" string, send an `error` SSE event with the appropriate copy ("This profile is private…") instead of a `done` event with 0 books.

### 4.8 Structured request logging

Replace `middleware.Logger` (which logs in a single text line) with a small custom middleware that emits `slog.Info` with request-id, method, path, status, duration, bytes. Add `middleware.RequestID` before it. This makes logs greppable in production.

### 4.9 Rate limiting

Add `github.com/go-chi/httprate` middleware:
- `/api/search`: 10 requests / minute / IP. (Search is expensive — one Goodreads fetch + N library API calls.)
- `/api/libraries`: 60 / minute / IP.
- `/api/recommendations`: 5 / minute / IP.
- `/api/shorten`: 30 / minute / IP.

Return 429 with `Retry-After` header. On the frontend, treat 429 in the SSE error path as "You're hitting this too fast, try again in a minute."

### 4.10 Graceful timeout for the SSE handler

`internal/handlers/search.go` `handleSSE`: cap the total wall-clock time at 90 seconds. If books are still streaming at 90s, send a `done` event with `message: "Stopped after 90s — some results may be missing."` rather than holding the connection forever. The per-book context is already 30s.

### 4.11 Health check expands

`/api/health` currently returns `{"status":"ok"}`. Have it also report:
- `gutenberg_loaded`: true/false (the catalog loads in background; users searching early won't see free books).
- `amazon_enabled`: from `amazonSvc.Enabled()`.
- `version`: a build-time `-ldflags "-X main.version=$(git rev-parse --short HEAD)"`.

This will surface "Gutenberg isn't ready yet" on the frontend (show a small status pill on results page if not loaded).

---

## Phase 5 — Polish details & little fixes

### 5.1 Sort/filter state survives page reload

Filter state currently doesn't persist (sort does via `benreadin_sort`). Persist active filters and filter mode the same way. Restore on results page load. Clear them when starting a brand-new search from `/`.

### 5.2 The "Refresh" button should communicate clearly

Tooltip is "Hard refresh — clear all caches and re-fetch from scratch". Rename to "Refresh availability". After clicking, show a small inline "Refreshing — this skips the cache" line above the progress bar so users understand why it's slower than the cached load.

### 5.3 Skeletons are too many / too few

The skeleton count is hardcoded to 12 (`results.js` `showSkeletons(12)`). On a 4-column desktop grid that's 3 rows; on mobile (1 column) that's 12 vertical cards = a full page of fake content before stubs arrive. Match the count to viewport: `Math.ceil(window.innerHeight / 140) + 2`.

### 5.4 Stagger animation on stubs feels slow on small shelves

`bookCard.js` `buildStubCard` adds `Math.min(index*25, 500)` ms delay. For shelves under 20 books the staggering is invisible; for 200+ books the last ones land 500ms after the first. Cap at `Math.min(index*15, 240)` and add a `prefers-reduced-motion: reduce` query that disables it.

### 5.5 Respect `prefers-reduced-motion`

Add at the top of `app.css`:
```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { animation-duration: 0.01ms !important; transition-duration: 0.01ms !important; }
}
```
Plus disable the progress-sweep gradient and the fadeUp keyframes inside the same query.

### 5.6 Theme toggle button doesn't reflect state

It just shows `🌙` always. After removing emoji (2.5), make it show a sun icon in dark mode and a moon icon in light mode. Add `aria-label="Switch to dark theme"` / `"Switch to light theme"` and update it on click.

### 5.7 Focus styles

Replace generic `outline: none` + custom box-shadow rings with a real `:focus-visible` outline (3px solid accent + 2px offset). Test tab order through the whole results page.

### 5.8 Accessible status announcements

Wrap `progress-label` with `aria-live="polite"` so screen readers announce progress updates. Wrap the error banner with `role="alert"` (already does — verify).

### 5.9 Goodreads cover URLs

Goodreads serves some covers over HTTP and some at low resolution (`SX98_`). Rewrite to:
- Force `https://`.
- Bump `SX98_` → `SY475_` (larger, still cached by Goodreads CDN) only when the URL matches that pattern. Keep the original as a fallback.
- Do this in `internal/services/goodreads.go` `itemToBook` so it happens once on the server, not per render.

### 5.10 Filter chip on `kindle` should not require an active library subscription

Currently the chip text reads "Kindle delivery". Some Libby libraries don't support Kindle delivery for the user's region. Add a small "?" affordance next to the chip that opens a tooltip explaining "Kindle delivery means Libby will send the book to your Kindle via your Amazon account. Not available in all libraries or regions."

### 5.11 Make the library autocomplete keyboard-only-friendly

- Currently `Tab` adds the highlighted item, but only if there's exactly one match (`search.js` line 209) or one is highlighted. Make `Tab` always commit the highlighted (or first) suggestion. `Enter` should commit and submit the form if a library is already added.
- The empty-input focus suggestions ("Recently used" / "Popular") render asynchronously. On a slow connection they appear after the user has already typed. Pre-fetch on first page render and cache in memory.

### 5.12 No-op clicks on the search button while streaming

The search button on `index.html` should be `disabled` once submit fires and the form has navigated away. Today, you can mash submit and queue navigations. Add `searchBtn.disabled = true` immediately on form submit before the navigation.

### 5.13 Print stylesheet

Add a `@media print` block: hide the header, footer, filter bar; show all books expanded; black-and-white safe. Users will print to PDF to take to the library.

### 5.14 `robots.txt` and `sitemap.xml`

Add minimal:
- `/public/robots.txt`: `User-agent: *` `Allow: /` `Disallow: /api/` `Disallow: /s/` `Sitemap: /sitemap.xml`.
- `/public/sitemap.xml`: just `/` and `/results.html` (skip parameterized URLs).

### 5.15 Goodreads ID can be a review ID, not a book ID

`internal/services/goodreads.go` `itemToBook` fallback (lines 204–210) parses the GUID, which is a `/review/show/{review-id}` URL. That returns the *review* ID, not the book ID. The cache key (`book_cache`) uses this ID — meaning the same book reviewed by two users would cache twice, and the share-link recommendations wouldn't dedupe correctly across users. The primary ID source (`item.BookID`) is correct; the fallback should be removed entirely (skip caching when book ID is missing).

### 5.16 Drop the default "Free Library of Philadelphia" pre-fill

`public/js/search.js` lines 266–269. It's a quirky author preference; new users get surprised. Drop it — let the form be empty and prompt the user via the empty-state suggestions.

---

## Phase 6 — Polish that requires more work but is worth it

### 6.1 Virtualize the book grid for huge shelves

Even with Phase 1 fixes, a 1000-book grid is heavy. Add windowed rendering:
- Maintain `allBooks` as before.
- Track scroll position; render only books in viewport ± 800px.
- When a card scrolls out, replace its DOM with a fixed-height div ("recycled").
- Reuse the same elements (don't create new DOM nodes per scroll).

Threshold: only kick in virtualization above 100 books. Smaller shelves don't need it.

### 6.2 Progressive enhancement: shelf links work without JS

The site is fully JS-dependent. Add a server-rendered HTML version of the results at `/results` (without `.html`) that streams the same data but as full HTML chunks (Go `html/template` server-side, no SSE). It can show a static (sorted by shelf order) view with no filtering. Users with JS off, or sharing links to non-techie friends, get a usable page. The current `/results.html` stays the JS interactive version.

### 6.3 Lite mode for mobile data

Allow `?lite=1` to skip Amazon, recommendations, and cover-image fetching. Useful for users on metered connections.

### 6.4 Service worker for offline-resilient navigation

Register a tiny service worker that caches `/`, `/results.html`, `/css/app.<hash>.css`, `/js/*.<hash>.js`, and the fonts. Make the search itself network-required (don't cache `/api/search`). Net effect: returning users see the UI shell instantly even on flaky connections.

### 6.5 Telemetry (optional, opt-in)

If the author wants any analytics, add Plausible (self-hosted) or a single-pixel beacon endpoint on the Go server. No third-party scripts. Respect `Do-Not-Track`. Skip if not wanted.

---

## Phase 7 — Operational readiness

### 7.1 systemd hardening

`benreadin.service` should include:
```
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/benreadin/data
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6
SystemCallArchitectures=native
LockPersonality=yes
MemoryDenyWriteExecute=yes
```
Verify with `systemd-analyze security benreadin`.

### 7.2 Dockerfile multi-stage with distroless

Current Dockerfile is fine but could use distroless base for a 15MB final image. Sketch:
```dockerfile
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" -o /out/benreadin ./cmd/benreadin

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/benreadin /benreadin
COPY public /public
COPY internal/db/schema.sql /internal/db/schema.sql
USER nonroot:nonroot
EXPOSE 3000
ENTRYPOINT ["/benreadin"]
```
Healthcheck via `docker-compose.yml`: `curl -fsS http://localhost:3000/api/health || exit 1` every 30s.

### 7.3 README updates

Add to README:
- "Make your Goodreads profile public" prerequisite as a clearly-flagged warning.
- Section on putting it behind a reverse proxy (nginx & Caddy snippets). Show the SSE-required `proxy_buffering off;` and `proxy_read_timeout` settings.
- Backup advice (just copy `data/cache.db` and `data/cache.db-wal`).
- Honest limits: "Tested up to 1000-book shelves; OverDrive API may rate-limit at higher concurrency."

### 7.4 GitHub Actions: CI that runs `go vet`, `go build`, `go test ./...`

Add `.github/workflows/ci.yml`. Single job, single matrix entry, Go 1.23. Cache the Go module cache. If/when tests are added, they run automatically. Catches obvious regressions before merge.

### 7.5 Add tests for the URL parser

`internal/services/urlparse.go` parses Goodreads URLs (numeric ID, shelf URL, OverReader URL, profile URL). This is the most user-facing parser; add table-driven tests in `internal/services/urlparse_test.go` covering each accepted format and a few malformed ones. Don't bother testing other services — they're network-dependent.

---

## Order of operations (suggested)

Implement in this order; each phase ends with a clean, mergeable commit:

1. **Phase 4.1, 4.2, 4.4, 4.8** (server: compression, cache headers, security headers, structured logs). Low risk, no UI changes.
2. **Phase 1** (perf). The single biggest UX win. Verify with a 200+ book shelf.
3. **Phase 2** (visuals). Biggest "show-off" win. Iterate with the author after the color/typo overhaul — they may want adjustments.
4. **Phase 3** (UX cuts + empty states). Quick once Phase 2 lands.
5. **Phase 5** (small fixes). Cherry-pick as time allows; none are blocking.
6. **Phase 4.5–4.11** (backend reliability). Most are quick.
7. **Phase 7** (ops). Set-and-forget once.
8. **Phase 6** (deeper polish). Optional — only if author wants further investment.

---

## Definition of done

A new visitor on a fresh device should be able to:
1. Land on `/`, understand what the app does in under 5 seconds without scrolling past the search card.
2. Type their library key, pick from autocomplete, paste a Goodreads URL, submit.
3. See the entire shelf as placeholder cards within 1 second of submitting.
4. See each book's availability fill in over the next 10–30 seconds with a clear progress indicator.
5. Sort/filter without any lag.
6. Share a link to the result that loads instantly for the recipient (cache + shortlink).
7. Encounter no console errors, no unstyled flashes, no jarring layout shifts.
8. On mobile: every tap target ≥ 36px, the page is readable in portrait without horizontal scroll.
9. Open the page in dark mode and have it match the OS preference automatically.
10. Be unable to identify, just from looking, that the UI was created with AI assistance.

If any of those ten points fails, the work isn't done.
