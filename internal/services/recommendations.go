package services

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
	"golang.org/x/sync/semaphore"
)

// RecProgress reports recommendation-engine progress for the SSE stream.
type RecProgress struct {
	Stage string `json:"stage"` // "profile" | "series" | "authors"
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// RecommendationService finds books from the user's taste profile that are
// available to borrow (with Kindle delivery) right now.
type RecommendationService struct {
	goodreads   *GoodreadsService
	overdrive   *OverDriveService
	openLibrary *OpenLibraryService
	gutenberg   *GutenbergService
	amazon      *AmazonService
	cache       *CacheService
	maxRecs     int
	maxAuthors  int
	sem         *semaphore.Weighted
}

func NewRecommendationService(
	gr *GoodreadsService,
	od *OverDriveService,
	ol *OpenLibraryService,
	gb *GutenbergService,
	az *AmazonService,
	cache *CacheService,
	maxRecs, maxAuthors int,
	concurrency int64,
) *RecommendationService {
	if concurrency <= 0 {
		concurrency = 8
	}
	return &RecommendationService{
		goodreads:   gr,
		overdrive:   od,
		openLibrary: ol,
		gutenberg:   gb,
		amazon:      az,
		cache:       cache,
		maxRecs:     maxRecs,
		maxAuthors:  maxAuthors,
		sem:         semaphore.NewWeighted(concurrency),
	}
}

// recCandidate is a Thunder match not yet enriched — enrichment (Open
// Library, Gutenberg, Amazon) is deferred until a candidate is actually
// chosen for emission, bounding those calls to at most maxRecs instead of
// every candidate considered.
type recCandidate struct {
	title, author, cover, isbn, description string
	libResults                              []models.LibraryResult
}

// recState tracks emitted recommendations across concurrent goroutines so
// series and author candidates dedupe against each other and against the
// shelves, and stop once the cap is reached.
type recState struct {
	mu    sync.Mutex
	seen  map[string]bool
	count int
	max   int
}

func newRecState(excluded []string, max int) *recState {
	seen := make(map[string]bool, len(excluded))
	for _, k := range excluded {
		seen[k] = true
	}
	return &recState{seen: seen, max: max}
}

// tryEmit claims key for a new recommendation. Returns false if it's already
// been recommended/shelved, or the cap has been reached.
func (rs *recState) tryEmit(key string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if key == "" || rs.count >= rs.max || rs.seen[key] {
		return false
	}
	rs.seen[key] = true
	rs.count++
	return true
}

func (rs *recState) full() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.count >= rs.max
}

func (rs *recState) isSeen(key string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.seen[key]
}

// Stream computes recommendations and delivers them via onRec as each is
// confirmed, reporting stage progress via onProgress. Blocks until the
// engine is done or ctx is canceled.
func (s *RecommendationService) Stream(
	ctx context.Context,
	userID string,
	libraryKeys []string,
	refresh bool,
	onRec func(models.Recommendation),
	onProgress func(RecProgress),
) error {
	profile, err := s.getProfile(ctx, userID, refresh)
	if err != nil {
		return err
	}
	onProgress(RecProgress{Stage: "profile", Done: 1, Total: 1})

	state := newRecState(profile.Excluded, s.maxRecs)

	s.runSeriesStage(ctx, profile.Series, libraryKeys, state, onRec, onProgress)
	if !state.full() {
		s.runAuthorStage(ctx, profile.Authors, libraryKeys, state, onRec, onProgress)
	}
	return nil
}

// getProfile returns the cached taste profile if fresh, otherwise builds one
// from the read + to-read shelves and caches it.
func (s *RecommendationService) getProfile(ctx context.Context, userID string, refresh bool) (*RecProfile, error) {
	if !refresh {
		var cached RecProfile
		if hit, err := s.cache.GetRecProfile(userID, &cached); err != nil {
			slog.Warn("rec profile cache read failed", "err", err)
		} else if hit {
			return &cached, nil
		}
	}

	read, err := s.goodreads.FetchShelf(ctx, userID, "read", refresh)
	if err != nil {
		return nil, fmt.Errorf("fetch read shelf: %w", err)
	}
	want, err := s.goodreads.FetchShelf(ctx, userID, "to-read", refresh)
	if err != nil {
		return nil, fmt.Errorf("fetch to-read shelf: %w", err)
	}

	profile := buildRecProfile(read, want)
	if err := s.cache.SetRecProfile(userID, profile); err != nil {
		slog.Warn("rec profile cache write failed", "err", err)
	}
	return profile, nil
}

// enrich turns a resolved Thunder match into a full Recommendation — same
// enrichment pipeline as a normal search result (Open Library ISBN/page
// count backfill, Gutenberg free-book match, Amazon pricing), so rec cards
// carry the same information and render with the same book-card component.
func (s *RecommendationService) enrich(ctx context.Context, c recCandidate) models.Recommendation {
	book := models.Book{
		Title:    c.title,
		Author:   c.author,
		CoverURL: c.cover,
		ISBN13:   c.isbn,
	}
	if c.description != "" {
		book.Description = truncate(stripHTML(c.description), 300)
	}

	enriched := s.openLibrary.Enrich(ctx, []models.Book{book})
	book = enriched[0]
	s.openLibrary.FetchRating(ctx, &book)

	gbResult := s.gutenberg.Lookup(book.SearchTitle(), book.Author)

	var amazonResult models.AmazonResult
	if s.amazon.Enabled() {
		if r, err := s.amazon.GetPrices(ctx, book); err != nil {
			slog.Debug("rec amazon prices failed", "title", book.Title, "err", err)
		} else {
			amazonResult = r
		}
	}

	return models.Recommendation{
		Book:            book,
		LibraryResults:  c.libResults,
		AmazonResult:    amazonResult,
		GutenbergResult: gbResult,
	}
}

// runSeriesStage resolves the next unread entry of each series in the
// profile and emits a recommendation for any that are available+Kindle at
// one of the given libraries.
func (s *RecommendationService) runSeriesStage(
	ctx context.Context,
	series []RecSeries,
	libraryKeys []string,
	state *recState,
	onRec func(models.Recommendation),
	onProgress func(RecProgress),
) {
	total := 0
	for _, sp := range series {
		if _, ok := sp.NextUnread(); ok {
			total++
		}
	}
	if total == 0 {
		return
	}

	var wg sync.WaitGroup
	var done int32
	var mu sync.Mutex
	reportDone := func() {
		mu.Lock()
		done++
		onProgress(RecProgress{Stage: "series", Done: int(done), Total: total})
		mu.Unlock()
	}

	for _, sp := range series {
		target, ok := sp.NextUnread()
		if !ok {
			continue
		}
		if state.full() {
			break
		}
		wg.Add(1)
		go func(sp RecSeries, target int) {
			defer wg.Done()
			defer reportDone()
			s.resolveSeriesEntry(ctx, sp, target, libraryKeys, state, onRec)
		}(sp, target)
	}
	wg.Wait()
}

func (s *RecommendationService) resolveSeriesEntry(
	ctx context.Context,
	sp RecSeries,
	target int,
	libraryKeys []string,
	state *recState,
	onRec func(models.Recommendation),
) {
	var mu sync.Mutex
	var cand recCandidate

	var wg sync.WaitGroup
	for _, libKey := range libraryKeys {
		wg.Add(1)
		go func(libKey string) {
			defer wg.Done()
			if err := s.sem.Acquire(ctx, 1); err != nil {
				return
			}
			defer s.sem.Release(1)

			items, err := s.overdrive.SearchMedia(ctx, libKey, sp.Name)
			if err != nil {
				slog.Debug("series search failed", "series", sp.Name, "library", libKey, "err", err)
				return
			}
			for _, it := range items {
				if !strings.EqualFold(strings.TrimSpace(it.DetailedSeries.SeriesName), sp.Name) {
					continue
				}
				order, ok := parseReadingOrder(it.DetailedSeries.ReadingOrder)
				if !ok || order != float64(target) {
					continue
				}
				mu.Lock()
				if cand.title == "" {
					cand.title, cand.author, cand.cover = it.Title, it.FirstCreatorName, it.coverURL()
					cand.isbn, cand.description = it.bestISBN(), it.Description
				}
				cand.libResults = append(cand.libResults, libraryResultFromThunderItem(libKey, it))
				mu.Unlock()
				return
			}
		}(libKey)
	}
	wg.Wait()

	if cand.title == "" || !anyAvailableKindle(cand.libResults) {
		return
	}
	key := recTitleKey(cand.title)
	if !state.tryEmit(key) {
		return
	}
	metrics.RecsGeneratedTotal.WithLabelValues("series").Inc()
	rec := s.enrich(ctx, cand)
	rec.Source = "series"
	rec.BecauseSeries = fmt.Sprintf("Next in %s — you finished #%d", sp.Name, sp.MaxRead)
	onRec(rec)
}

// runAuthorStage fetches each profile author's candidates concurrently, then
// emits them round-robin (so one prolific author doesn't dominate the list),
// most-finished author first.
func (s *RecommendationService) runAuthorStage(
	ctx context.Context,
	authors []RecAuthor,
	libraryKeys []string,
	state *recState,
	onRec func(models.Recommendation),
	onProgress func(RecProgress),
) {
	n := len(authors)
	if n > s.maxAuthors {
		n = s.maxAuthors
	}
	if n == 0 {
		return
	}
	authors = authors[:n]

	candidateLists := make([][]recCandidate, n)
	var wg sync.WaitGroup
	var done int32
	var mu sync.Mutex
	for i, a := range authors {
		wg.Add(1)
		go func(i int, a RecAuthor) {
			defer wg.Done()
			candidateLists[i] = s.authorCandidates(ctx, a, libraryKeys, state)
			mu.Lock()
			done++
			onProgress(RecProgress{Stage: "authors", Done: int(done), Total: n})
			mu.Unlock()
		}(i, a)
	}
	wg.Wait()

	// Round-robin across authors so the strongest-weighted author's picks
	// lead, but no single author floods the list.
	for idx := 0; ; idx++ {
		emittedAny := false
		for i := range candidateLists {
			if state.full() {
				return
			}
			if idx >= len(candidateLists[i]) {
				continue
			}
			cand := candidateLists[i][idx]
			key := recTitleKey(cand.title)
			if !state.tryEmit(key) {
				continue
			}
			metrics.RecsGeneratedTotal.WithLabelValues("author").Inc()
			rec := s.enrich(ctx, cand)
			rec.Source = "author"
			rec.BecauseAuthor = fmt.Sprintf("You finished %d book%s by %s", authors[i].Finished, plural(authors[i].Finished), authors[i].Name)
			onRec(rec)
			emittedAny = true
		}
		if !emittedAny {
			return
		}
	}
}

// authorCandidates searches every library for the author, keeping items
// whose creator matches and title isn't excluded, merging the same book's
// results across libraries. Order follows Thunder's relevance ranking from
// the first library where each title appeared.
func (s *RecommendationService) authorCandidates(ctx context.Context, a RecAuthor, libraryKeys []string, state *recState) []recCandidate {
	matchKey := authorMatchKey(a.Name)
	if matchKey == "" {
		return nil
	}
	query := authorQueryName(a.Name)

	var mu sync.Mutex
	order := []string{}
	byKey := map[string]*recCandidate{}

	var wg sync.WaitGroup
	for _, libKey := range libraryKeys {
		wg.Add(1)
		go func(libKey string) {
			defer wg.Done()
			if err := s.sem.Acquire(ctx, 1); err != nil {
				return
			}
			defer s.sem.Release(1)

			items, err := s.overdrive.SearchMedia(ctx, libKey, query)
			if err != nil {
				slog.Debug("author search failed", "author", a.Name, "library", libKey, "err", err)
				return
			}
			for _, it := range items {
				if authorMatchKey(it.FirstCreatorName) != matchKey {
					continue
				}
				key := recTitleKey(it.Title)
				if key == "" || state.isSeen(key) {
					continue
				}
				mu.Lock()
				cand, exists := byKey[key]
				if !exists {
					cand = &recCandidate{
						title:       it.Title,
						author:      it.FirstCreatorName,
						cover:       it.coverURL(),
						isbn:        it.bestISBN(),
						description: it.Description,
					}
					byKey[key] = cand
					order = append(order, key)
				}
				cand.libResults = append(cand.libResults, libraryResultFromThunderItem(libKey, it))
				mu.Unlock()
			}
		}(libKey)
	}
	wg.Wait()

	out := make([]recCandidate, 0, len(order))
	for _, key := range order {
		cand := byKey[key]
		if anyAvailableKindle(cand.libResults) {
			out = append(out, *cand)
		}
	}
	return out
}

func anyAvailableKindle(results []models.LibraryResult) bool {
	for _, lr := range results {
		if lr.Status == models.StatusAvailable && lr.HasKindle {
			return true
		}
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// parseReadingOrder parses Thunder's detailedSeries.readingOrder ("20",
// "3.5") and reports whether it's a whole-number entry — fractional entries
// are novella side-stories, not the mainline "next book".
func parseReadingOrder(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	if v != math.Trunc(v) {
		return 0, false
	}
	return v, true
}
