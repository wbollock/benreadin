package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wbollock/benreadin/internal/models"
	"github.com/wbollock/benreadin/internal/services"
	"golang.org/x/sync/semaphore"
)

// SearchRequest is the JSON body for POST /api/search.
type SearchRequest struct {
	URL       string   `json:"url"`
	Libraries []string `json:"libraries"`
}

// BookEvent is a single SSE payload sent to the client.
type BookEvent struct {
	Book            models.Book             `json:"book"`
	LibraryResults  []models.LibraryResult  `json:"library_results"`
	AmazonResult    models.AmazonResult     `json:"amazon_result"`
	GutenbergResult *models.GutenbergResult `json:"gutenberg_result,omitempty"`
}

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
}

func NewSearchHandler(
	gr *services.GoodreadsService,
	od *services.OverDriveService,
	ol *services.OpenLibraryService,
	az *services.AmazonService,
	recs *services.RecommendationService,
	gb *services.GutenbergService,
	cache *services.CacheService,
	odConcurrency, azConcurrency int64,
) *SearchHandler {
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
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
	books, err := h.goodreads.FetchShelf(ctx, parsed.UserID, parsed.Shelf)
	if err != nil {
		sendEvent("error", map[string]string{"message": fmt.Sprintf("Failed to fetch Goodreads shelf: %s", err)})
		return
	}

	if len(books) == 0 {
		sendEvent("done", map[string]interface{}{"total": 0, "message": "No books found on shelf"})
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

	// Phase 2: resolve cache hits immediately and collect misses for the pipeline.
	completedCount := 0
	var toFetch []models.Book
	for _, book := range books {
		if !hardRefresh && book.GoodreadsID != "" {
			var cached BookEvent
			if hit, _ := h.cache.GetBook(book.GoodreadsID, libsKey, &cached); hit {
				slog.Debug("book cache hit", "book", book.Title)
				completedCount++
				sendEvent("book", cached)
				sendEvent("progress", ProgressEvent{Total: len(books), Completed: completedCount})
				continue
			}
		}
		toFetch = append(toFetch, book)
	}

	// Phase 3: enrich + check each cache-miss book concurrently, replacing their stubs.
	// OL enrichment and OD availability checks run in parallel per book —
	// OD uses whatever ISBN Goodreads provided (or title+author fallback) while
	// OL enrichment fills in page count and a better ISBN in the background.
	resultCh := make(chan result, len(toFetch))
	var wg sync.WaitGroup
	for _, book := range toFetch {
		book := book
		wg.Add(1)
		go func() {
			defer wg.Done()

			bookCtx, bookCancel := context.WithTimeout(ctx, 30*time.Second)
			defer bookCancel()

			// OL enrichment runs concurrently with OD checks.
			var enrichedBook models.Book
			var olWg sync.WaitGroup
			olWg.Add(1)
			go func() {
				defer olWg.Done()
				enriched := h.openLibrary.Enrich(bookCtx, []models.Book{book})
				enrichedBook = enriched[0]
			}()

			// OD availability checks start immediately using current book data.
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

			// Wait for OL enrichment to finish before assembling the result.
			olWg.Wait()
			book = enrichedBook

			var azResult models.AmazonResult
			if err := h.azSem.Acquire(bookCtx, 1); err == nil {
				azResult, err = h.amazon.GetPrices(bookCtx, book)
				h.azSem.Release(1)
				if err != nil {
					slog.Warn("amazon prices failed", "book", book.Title, "err", err)
				}
			}

			gbResult := h.gutenberg.Lookup(book.Title, book.Author)

			res := result{
				book:            book,
				libraryResults:  libResults,
				amazonResult:    azResult,
				gutenbergResult: gbResult,
			}

			if bookCtx.Err() == nil && book.GoodreadsID != "" {
				if err := h.cache.SetBook(book.GoodreadsID, libsKey, BookEvent{
					Book:            res.book,
					LibraryResults:  res.libraryResults,
					AmazonResult:    res.amazonResult,
					GutenbergResult: res.gutenbergResult,
				}); err != nil {
					slog.Warn("book cache set failed", "book", book.Title, "err", err)
				}
			}

			resultCh <- res
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for res := range resultCh {
		if ctx.Err() != nil {
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
