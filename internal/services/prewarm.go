package services

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wbollock/benreadin/internal/models"
	"golang.org/x/sync/semaphore"
)

// Prewarm concurrency is deliberately conservative: this is background work
// competing with live searches for the same upstream APIs.
const (
	prewarmBookWorkers   = 4  // books resolved concurrently per run
	prewarmODConcurrency = 10 // OverDrive availability checks in flight
	prewarmAZConcurrency = 1  // Amazon PA-API calls in flight
	prewarmStartupDelay  = 30 * time.Second
)

// PrewarmTarget identifies one shelf + library set to keep warm.
type PrewarmTarget struct {
	UserID    string
	Shelf     string
	Libraries []string
}

// LibrariesKey returns the stable, sorted cache key for a library set — the
// same key format the search handler uses for book_cache rows.
func LibrariesKey(libraries []string) string {
	sorted := make([]string, len(libraries))
	copy(sorted, libraries)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// PrewarmService periodically re-runs recent searches in the background so
// book_cache is already warm when a returning user searches again. Targets are
// the configured seeds plus every search recorded within the active window.
type PrewarmService struct {
	goodreads   *GoodreadsService
	overdrive   *OverDriveService
	openLibrary *OpenLibraryService
	amazon      *AmazonService
	gutenberg   *GutenbergService
	cache       *CacheService

	seeds        []PrewarmTarget
	interval     time.Duration
	activeWindow time.Duration
	bookTTL      int64

	odSem *semaphore.Weighted
	azSem *semaphore.Weighted
}

func NewPrewarmService(
	gr *GoodreadsService,
	od *OverDriveService,
	ol *OpenLibraryService,
	az *AmazonService,
	gb *GutenbergService,
	cache *CacheService,
	seeds []PrewarmTarget,
	interval, activeWindow time.Duration,
	bookTTL int64,
) *PrewarmService {
	return &PrewarmService{
		goodreads:    gr,
		overdrive:    od,
		openLibrary:  ol,
		amazon:       az,
		gutenberg:    gb,
		cache:        cache,
		seeds:        seeds,
		interval:     interval,
		activeWindow: activeWindow,
		bookTTL:      bookTTL,
		odSem:        semaphore.NewWeighted(prewarmODConcurrency),
		azSem:        semaphore.NewWeighted(prewarmAZConcurrency),
	}
}

// Start runs the prewarm loop until ctx is cancelled. Call in a goroutine.
func (p *PrewarmService) Start(ctx context.Context) {
	select {
	case <-time.After(prewarmStartupDelay):
	case <-ctx.Done():
		return
	}
	p.runOnce(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.runOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (p *PrewarmService) runOnce(ctx context.Context) {
	now := time.Now()
	cutoff := now.Add(-p.activeWindow).Unix()

	if err := p.cache.PurgeRecentSearches(cutoff); err != nil {
		slog.Warn("prewarm: purge recent searches failed", "err", err)
	}

	recent, err := p.cache.RecentSearches(cutoff)
	if err != nil {
		slog.Warn("prewarm: load recent searches failed", "err", err)
	}

	targets := make([]PrewarmTarget, 0, len(p.seeds)+len(recent))
	seen := map[string]bool{}
	add := func(t PrewarmTarget) {
		if t.UserID == "" || len(t.Libraries) == 0 {
			return
		}
		key := t.UserID + "\x00" + t.Shelf + "\x00" + LibrariesKey(t.Libraries)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, t)
	}
	for _, s := range p.seeds {
		add(s)
	}
	for _, rs := range recent {
		add(PrewarmTarget{UserID: rs.UserID, Shelf: rs.Shelf, Libraries: strings.Split(rs.LibrariesKey, ",")})
	}

	if len(targets) == 0 {
		return
	}

	slog.Info("prewarm: run starting", "targets", len(targets))
	start := time.Now()
	var refreshed, skipped int
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		r, s := p.prewarmTarget(ctx, t)
		refreshed += r
		skipped += s
	}
	slog.Info("prewarm: run complete",
		"targets", len(targets), "refreshed", refreshed, "fresh", skipped,
		"took", time.Since(start).Round(time.Second))
}

// prewarmTarget refreshes the book cache for one shelf + library set and
// returns (refreshed, stillFresh) book counts.
func (p *PrewarmService) prewarmTarget(ctx context.Context, t PrewarmTarget) (int, int) {
	books, err := p.goodreads.FetchShelf(ctx, t.UserID, t.Shelf, false)
	if err != nil {
		slog.Warn("prewarm: shelf fetch failed", "user", t.UserID, "shelf", t.Shelf, "err", err)
		return 0, 0
	}

	libsKey := LibrariesKey(t.Libraries)

	// Refresh entries that would expire before the next run, so a user landing
	// between runs always finds a live cache row. If the interval exceeds the
	// TTL this refreshes everything each run.
	staleAt := time.Now().Unix() - (p.bookTTL - int64(p.interval/time.Second))

	var toFetch []models.Book
	skipped := 0
	for _, b := range books {
		if b.GoodreadsID == "" {
			continue // uncacheable — a live search will resolve it
		}
		fetchedAt, found, err := p.cache.BookFetchedAt(b.GoodreadsID, libsKey)
		if err == nil && found && fetchedAt > staleAt {
			skipped++
			continue
		}
		toFetch = append(toFetch, b)
	}

	if len(toFetch) == 0 {
		return 0, skipped
	}

	var wg sync.WaitGroup
	workCh := make(chan models.Book)
	var mu sync.Mutex
	refreshed := 0
	for range prewarmBookWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for book := range workCh {
				if p.prewarmBook(ctx, book, t.Libraries, libsKey) {
					mu.Lock()
					refreshed++
					mu.Unlock()
				}
			}
		}()
	}
	for _, b := range toFetch {
		if ctx.Err() != nil {
			break
		}
		workCh <- b
	}
	close(workCh)
	wg.Wait()

	return refreshed, skipped
}

// prewarmBook resolves one book exactly like a live search — OverDrive
// availability per library, OpenLibrary enrichment, Amazon prices, Gutenberg
// match — and writes the result to book_cache. Returns true on success.
func (p *PrewarmService) prewarmBook(ctx context.Context, book models.Book, libraries []string, libsKey string) bool {
	bookCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	enriched := p.openLibrary.Enrich(bookCtx, []models.Book{book})[0]

	libResults := make([]models.LibraryResult, len(libraries))
	var libWg sync.WaitGroup
	for i, lib := range libraries {
		libResults[i] = models.LibraryResult{LibraryKey: lib, Status: models.StatusNotFound}
		libWg.Add(1)
		go func(i int, lib string) {
			defer libWg.Done()
			if err := p.odSem.Acquire(bookCtx, 1); err != nil {
				return
			}
			defer p.odSem.Release(1)
			lr, err := p.overdrive.CheckAvailability(bookCtx, book, lib)
			if err != nil {
				slog.Warn("prewarm: overdrive check failed", "book", book.Title, "library", lib, "err", err)
				return
			}
			libResults[i] = lr
		}(i, lib)
	}
	libWg.Wait()

	enriched.Genre = models.GenreFromResults(libResults)

	var azResult models.AmazonResult
	if err := p.azSem.Acquire(bookCtx, 1); err == nil {
		var err error
		azResult, err = p.amazon.GetPrices(bookCtx, enriched)
		p.azSem.Release(1)
		if err != nil {
			slog.Warn("prewarm: amazon prices failed", "book", enriched.Title, "err", err)
		}
	}

	if bookCtx.Err() != nil {
		return false // partial results — don't poison the cache
	}

	event := models.BookEvent{
		Book:            enriched,
		LibraryResults:  libResults,
		AmazonResult:    azResult,
		GutenbergResult: p.gutenberg.Lookup(enriched.Title, enriched.Author),
	}
	if err := p.cache.SetBook(enriched.GoodreadsID, libsKey, event); err != nil {
		slog.Warn("prewarm: cache set failed", "book", enriched.Title, "err", err)
		return false
	}
	return true
}

// ParsePrewarmSeeds parses the PREWARM_SEEDS env format:
// "userID:shelf:lib1,lib2;userID2:shelf2:lib3". Malformed entries are skipped
// with a warning.
func ParsePrewarmSeeds(raw string) []PrewarmTarget {
	var out []PrewarmTarget
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
			slog.Warn("prewarm: skipping malformed seed", "seed", entry)
			continue
		}
		shelf := parts[1]
		if shelf == "" {
			shelf = "to-read"
		}
		out = append(out, PrewarmTarget{
			UserID:    parts[0],
			Shelf:     shelf,
			Libraries: splitComma(parts[2]),
		})
	}
	return out
}
