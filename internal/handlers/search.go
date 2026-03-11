package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/wbollock/shelfprice/internal/models"
	"github.com/wbollock/shelfprice/internal/services"
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

	sendEvent := func(eventType string, data interface{}) {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(b))
		flusher.Flush()
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

	// Fan-out: enrich + check each book concurrently.
	// Open Library enrichment and OverDrive/Amazon checks are pipelined per book
	// so we don't wait for all enrichment before starting availability checks.
	type result struct {
		book            models.Book
		libraryResults  []models.LibraryResult
		amazonResult    models.AmazonResult
		gutenbergResult *models.GutenbergResult
	}

	resultCh := make(chan result, len(books))
	var wg sync.WaitGroup

	libsKey := librariesCacheKey(libraries)
	hardRefresh := r.URL.Query().Get("refresh") == "true"

	for _, book := range books {
		book := book
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Check the per-book result cache first (skipped on hard refresh).
			if !hardRefresh && book.GoodreadsID != "" {
				var cached BookEvent
				if hit, _ := h.cache.GetBook(book.GoodreadsID, libsKey, &cached); hit {
					slog.Debug("book cache hit", "book", book.Title)
					resultCh <- result{
						book:           cached.Book,
						libraryResults: cached.LibraryResults,
						amazonResult:   cached.AmazonResult,
					}
					return
				}
			}

			// Cache miss — run the full pipeline.

			// Enrich this book (Open Library ISBN13 + cover)
			enriched := h.openLibrary.Enrich(ctx, []models.Book{book})
			book = enriched[0]

			// Library availability fan-out
			libResults := make([]models.LibraryResult, len(libraries))
			var libWg sync.WaitGroup
			for i, lib := range libraries {
				lib := lib
				i := i
				libWg.Add(1)
				go func() {
					defer libWg.Done()
					if err := h.odSem.Acquire(ctx, 1); err != nil {
						return
					}
					defer h.odSem.Release(1)

					lr, err := h.overdrive.CheckAvailability(ctx, book, lib)
					if err != nil {
						slog.Warn("overdrive check failed", "book", book.Title, "library", lib, "err", err)
						lr = models.LibraryResult{LibraryKey: lib, Status: models.StatusNotFound}
					}
					libResults[i] = lr
				}()
			}
			libWg.Wait()

			// Amazon pricing
			var azResult models.AmazonResult
			if err := h.azSem.Acquire(ctx, 1); err == nil {
				azResult, err = h.amazon.GetPrices(ctx, book)
				h.azSem.Release(1)
				if err != nil {
					slog.Warn("amazon prices failed", "book", book.Title, "err", err)
				}
			}

			// Gutenberg lookup (fast local SQLite — no semaphore needed).
			gbResult := h.gutenberg.Lookup(book.Title, book.Author)

			res := result{
				book:            book,
				libraryResults:  libResults,
				amazonResult:    azResult,
				gutenbergResult: gbResult,
			}

			// Store to book cache for future searches.
			if book.GoodreadsID != "" {
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

	// Close channel when all goroutines done
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect enriched books for the recommendations pass.
	enrichedBooks := make([]models.Book, 0, len(books))

	completed := 0
	for res := range resultCh {
		if ctx.Err() != nil {
			return
		}
		completed++
		enrichedBooks = append(enrichedBooks, res.book)
		sendEvent("book", BookEvent{
			Book:            res.book,
			LibraryResults:  res.libraryResults,
			AmazonResult:    res.amazonResult,
			GutenbergResult: res.gutenbergResult,
		})
		sendEvent("progress", ProgressEvent{
			Total:     len(books),
			Completed: completed,
		})
	}

	// Find recommendations before sending done so the client connection is still open.
	sendEvent("progress", ProgressEvent{Message: "Finding recommendations..."})
	recs := h.recommendations.FindRecommendations(ctx, enrichedBooks, libraries)
	if len(recs) > 0 {
		sendEvent("recommendations", recs)
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
