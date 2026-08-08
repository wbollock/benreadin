package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
	"github.com/wbollock/benreadin/internal/services"
	"golang.org/x/sync/semaphore"
)

// ssePadding is a high-entropy comment payload sent on connect so the stream
// crosses Firefox's ~1KB threshold for dispatching the first EventSource events,
// even if an intermediary recompresses the response. Built once at startup;
// printable ASCII only, so it never contains a newline that would end the SSE
// comment early.
var ssePadding = func() string {
	const n = 2048
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('!' + rand.Intn('~'-'!'+1)) // '!'..'~', no '\n'
	}
	return string(b)
}()

// SearchRequest is the JSON body for POST /api/search.
type SearchRequest struct {
	URL       string   `json:"url"`
	Libraries []string `json:"libraries"`
}

// BookEvent is a single SSE payload sent to the client. It lives in models so
// the prewarm scheduler writes the identical shape to the book cache.
type BookEvent = models.BookEvent

// BookStubsEvent is sent as a single batch immediately after the Goodreads
// shelf is fetched, so the client can render the full list of placeholder cards
// at once before any availability checks complete.
type BookStubsEvent struct {
	Books []models.Book `json:"books"`
}

// ProgressEvent reports overall search progress.
type ProgressEvent struct {
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Message   string `json:"message,omitempty"`
}

// SearchHandler handles POST /api/search and streams results as SSE.
type SearchHandler struct {
	goodreads       *services.GoodreadsService
	overdrive       *services.OverDriveService
	openLibrary     *services.OpenLibraryService
	amazon          *services.AmazonService
	recommendations *services.RecommendationService
	gutenberg       *services.GutenbergService
	cache           *services.CacheService
	odSem           *semaphore.Weighted
	azSem           *semaphore.Weighted
	odPerSearch     int64
}

func NewSearchHandler(
	gr *services.GoodreadsService,
	od *services.OverDriveService,
	ol *services.OpenLibraryService,
	az *services.AmazonService,
	recs *services.RecommendationService,
	gb *services.GutenbergService,
	cache *services.CacheService,
	odConcurrency, odPerSearch, azConcurrency int64,
) *SearchHandler {
	if odPerSearch <= 0 || odPerSearch > odConcurrency {
		odPerSearch = odConcurrency
	}
	return &SearchHandler{
		goodreads:       gr,
		overdrive:       od,
		openLibrary:     ol,
		amazon:          az,
		recommendations: recs,
		gutenberg:       gb,
		cache:           cache,
		odSem:           semaphore.NewWeighted(odConcurrency),
		azSem:           semaphore.NewWeighted(azConcurrency),
		odPerSearch:     odPerSearch,
	}
}

// librariesCacheKey returns a stable, sorted cache key for a library set.
func librariesCacheKey(libraries []string) string {
	sorted := make([]string, len(libraries))
	copy(sorted, libraries)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func (h *SearchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// SSE connection — expects ?url=...&libraries=...&libraries=...
		h.handleSSE(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (h *SearchHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	metrics.ActiveStreams.Inc()
	defer metrics.ActiveStreams.Dec()
	outcome := "error"
	defer func() { metrics.SearchesTotal.WithLabelValues(outcome).Inc() }()

	w.Header().Set("Content-Type", "text/event-stream")
	// no-transform stops intermediaries (carrier proxies, CDNs) from buffering
	// or recompressing the stream, which would defeat per-event flushing.
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var sseMu sync.Mutex
	sendEvent := func(eventType string, data interface{}) {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		sseMu.Lock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(b))
		flusher.Flush()
		sseMu.Unlock()
	}

	// Some browsers won't surface the response to EventSource until a minimum
	// number of bytes has arrived — Firefox needs ~1KB before it dispatches the
	// first events (Chrome has no such threshold), so the first tiny event plus a
	// slow FetchShelf leaves the client frozen on "Starting…". A 2KB comment shoves
	// past that threshold and forces an immediate flush.
	//
	// The padding MUST be high-entropy: a run of identical spaces compresses to a
	// few bytes, and an intermediary that recompresses the stream despite
	// no-transform (a carrier proxy or upstream CDN) would shrink it back below the
	// threshold — which surfaces on mobile Firefox while mobile Chrome, lacking the
	// threshold, still works. Random bytes stay ~2KB through any recompression.
	sseMu.Lock()
	fmt.Fprintf(w, ":%s\n\n", ssePadding)
	flusher.Flush()
	sseMu.Unlock()

	ctx := r.Context()

	// Parse request params from query string (GET SSE)
	rawURL := r.URL.Query().Get("url")
	libraries := r.URL.Query()["libraries"]

	if rawURL == "" {
		sendEvent("error", map[string]string{"message": "url parameter is required"})
		return
	}

	// Parse the shelf URL
	parsed, err := services.ParseShelfURL(rawURL)
	if err != nil {
		sendEvent("error", map[string]string{"message": err.Error()})
		return
	}

	// An explicit shelf param (home-page shelf picker) overrides whatever the
	// URL implied.
	if shelf := strings.TrimSpace(r.URL.Query().Get("shelf")); shelf != "" {
		parsed.Shelf = shelf
	}

	// If libraries were passed in query, use those; otherwise use parsed ones
	if len(libraries) == 0 {
		libraries = parsed.Libraries
	}
	if len(libraries) == 0 {
		sendEvent("error", map[string]string{"message": "at least one library key is required"})
		return
	}

	sendEvent("progress", ProgressEvent{Message: "Fetching your Goodreads shelf..."})

	// Fetch books from Goodreads
	books, err := h.goodreads.FetchShelf(ctx, parsed.UserID, parsed.Shelf, r.URL.Query().Get("refresh") == "true")
	if err != nil {
		msg := fmt.Sprintf("Failed to fetch Goodreads shelf: %s", err)
		var statusErr *services.GoodreadsStatusError
		if errors.As(err, &statusErr) {
			msg = statusErr.Error() // already worded for the end user
		}
		sendEvent("error", map[string]string{"message": msg})
		return
	}

	// Remember this search so the prewarm scheduler keeps its results warm for
	// returning users.
	if err := h.cache.RecordSearch(parsed.UserID, parsed.Shelf, librariesCacheKey(libraries)); err != nil {
		slog.Warn("record recent search failed", "err", err)
	}

	if len(books) == 0 {
		outcome = "ok"
		sendEvent("done", map[string]interface{}{
			"total":   0,
			"message": "No books found — the shelf may be empty, or the Goodreads profile may be private (Settings → Privacy).",
		})
		return
	}

	sendEvent("progress", ProgressEvent{
		Total:   len(books),
		Message: fmt.Sprintf("Found %d books — checking availability...", len(books)),
	})

	// Heartbeat: keeps the SSE connection alive through proxies that close idle connections.
	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sseMu.Lock()
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
				sseMu.Unlock()
			case <-streamDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	type result struct {
		book            models.Book
		libraryResults  []models.LibraryResult
		amazonResult    models.AmazonResult
		gutenbergResult *models.GutenbergResult
	}

	libsKey := librariesCacheKey(libraries)
	hardRefresh := r.URL.Query().Get("refresh") == "true"

	// Phase 1: send the entire book list as a single batch stub event so the
	// client can render all placeholder cards at once — no drip, no waiting.
	sendEvent("book_stubs", BookStubsEvent{Books: books})

	// Phase 2: resolve cache hits immediately and collect misses for the
	// pipeline. One batched query for the whole shelf — per-book lookups cost
	// hundreds of round trips before the first cached card reaches the client.
	var cachedEvents map[string]json.RawMessage
	if !hardRefresh {
		ids := make([]string, 0, len(books))
		for _, book := range books {
			if book.GoodreadsID != "" {
				ids = append(ids, book.GoodreadsID)
			}
		}
		var err error
		if cachedEvents, err = h.cache.GetBooks(ids, libsKey); err != nil {
			slog.Warn("book cache batch read failed", "err", err)
		}
	}

	completedCount := 0
	var toFetch []models.Book
	for _, book := range books {
		if cached, ok := cachedEvents[book.GoodreadsID]; ok && book.GoodreadsID != "" {
			completedCount++
			sendEvent("book", cached)
			sendEvent("progress", ProgressEvent{Total: len(books), Completed: completedCount})
			continue
		}
		toFetch = append(toFetch, book)
	}

	metrics.BooksCheckedTotal.WithLabelValues("cache").Add(float64(len(books) - len(toFetch)))
	metrics.BooksCheckedTotal.WithLabelValues("fetched").Add(float64(len(toFetch)))

	// Phase 3: check availability for each cache-miss book concurrently and
	// stream its "book" event the moment the OverDrive checks finish. The slow
	// metadata work — OpenLibrary enrichment (6-wide global cap) and Amazon
	// prices (3-wide global cap, two sequential API calls per book) — runs
	// afterwards per book and streams as "book_update" patches, so availability
	// is never queued behind price lookups. On a large shelf that queue is
	// minutes long; availability itself is only seconds.
	resultCh := make(chan result, len(toFetch))
	updateCh := make(chan BookEvent, len(toFetch))
	// Fairness: the global odSem is first-come-first-served, so without a
	// per-search ceiling one huge shelf occupies every slot and starves other
	// users' searches until it drains.
	searchSem := semaphore.NewWeighted(h.odPerSearch)
	var availWg, fullWg sync.WaitGroup
	for _, book := range toFetch {
		book := book
		availWg.Add(1)
		fullWg.Add(1)
		go func() {
			defer fullWg.Done()

			bookCtx, bookCancel := context.WithTimeout(ctx, 30*time.Second)
			defer bookCancel()

			// OD availability checks use whatever ISBN Goodreads provided
			// (or title+author fallback).
			libResults := make([]models.LibraryResult, len(libraries))
			for i := range libResults {
				libResults[i] = models.LibraryResult{LibraryKey: libraries[i], Status: models.StatusNotFound}
			}
			var libWg sync.WaitGroup
			for i, lib := range libraries {
				lib := lib
				i := i
				libWg.Add(1)
				go func() {
					defer libWg.Done()
					if err := searchSem.Acquire(bookCtx, 1); err != nil {
						return
					}
					defer searchSem.Release(1)
					if err := h.odSem.Acquire(bookCtx, 1); err != nil {
						return
					}
					defer h.odSem.Release(1)
					lr, err := h.overdrive.CheckAvailability(bookCtx, book, lib)
					if err != nil {
						slog.Warn("overdrive check failed", "book", book.Title, "library", lib, "err", err)
						lr = models.LibraryResult{LibraryKey: lib, Status: models.StatusNotFound}
					}
					libResults[i] = lr
				}()
			}
			libWg.Wait()

			gbResult := h.gutenberg.Lookup(book.SearchTitle(), book.Author)

			resultCh <- result{
				book:            book,
				libraryResults:  libResults,
				gutenbergResult: gbResult,
			}
			availWg.Done()

			// Enrichment: OL fills ISBN-13/page count (feeds a better Amazon
			// lookup), then Amazon prices. Both queues drain slowly on large
			// shelves, so this gets its own generous timeout independent of the
			// 30s availability budget — under the old shared budget the tail of
			// a big shelf timed out before ever reaching the Amazon semaphore.
			enrichCtx, enrichCancel := context.WithTimeout(ctx, 3*time.Minute)
			defer enrichCancel()

			enriched := h.openLibrary.Enrich(enrichCtx, []models.Book{book})[0]

			var azResult models.AmazonResult
			if err := h.azSem.Acquire(enrichCtx, 1); err == nil {
				azResult, err = h.amazon.GetPrices(enrichCtx, enriched)
				h.azSem.Release(1)
				if err != nil {
					metrics.UpstreamErrorsTotal.WithLabelValues("amazon").Inc()
					slog.Warn("amazon prices failed", "book", enriched.Title, "err", err)
				}
			}

			full := BookEvent{
				Book:            enriched,
				LibraryResults:  libResults,
				AmazonResult:    azResult,
				GutenbergResult: gbResult,
			}

			if enrichCtx.Err() == nil && enriched.GoodreadsID != "" {
				if err := h.cache.SetBook(enriched.GoodreadsID, libsKey, full); err != nil {
					slog.Warn("book cache set failed", "book", enriched.Title, "err", err)
				}
			}

			// Only patch the client if enrichment actually added something.
			if enriched != book || azResult != (models.AmazonResult{}) {
				updateCh <- full
			}
		}()
	}

	go func() {
		availWg.Wait()
		close(resultCh)
	}()
	go func() {
		fullWg.Wait()
		close(updateCh)
	}()

	for res := range resultCh {
		if ctx.Err() != nil {
			outcome = "canceled"
			return
		}
		completedCount++
		sendEvent("book", BookEvent{
			Book:            res.book,
			LibraryResults:  res.libraryResults,
			AmazonResult:    res.amazonResult,
			GutenbergResult: res.gutenbergResult,
		})
		sendEvent("progress", ProgressEvent{
			Total:     len(books),
			Completed: completedCount,
		})
	}

	// All availability is on screen — let the client finish its progress UI
	// while metadata/price patches keep streaming below.
	if len(toFetch) > 0 && ctx.Err() == nil {
		sendEvent("availability_done", map[string]interface{}{
			"total":   len(books),
			"message": fmt.Sprintf("Checked %d books — filling in details…", len(books)),
		})
	}

	for ev := range updateCh {
		if ctx.Err() != nil {
			outcome = "canceled"
			return
		}
		sendEvent("book_update", ev)
	}

	outcome = "ok"
	sendEvent("done", map[string]interface{}{
		"total":   len(books),
		"message": fmt.Sprintf("Done — checked %d books", len(books)),
	})
}

func plural(singular, pluralForm string, n int) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
