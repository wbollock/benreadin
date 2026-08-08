package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
)

const thunderRetryDelay = 250 * time.Millisecond

const thunderBase = "https://thunder.api.overdrive.com/v2/libraries/%s/media"

// OverDriveService queries the OverDrive Thunder API for library availability.
type OverDriveService struct {
	client *http.Client
	cache  *CacheService
	flight singleflight.Group
}

func NewOverDriveService(cache *CacheService) *OverDriveService {
	return &OverDriveService{
		// Idle pool sized to CONCURRENCY_OVERDRIVE's default so parallel
		// availability checks reuse warm connections to the Thunder API.
		client: newHTTPClient(10*time.Second, 50),
		cache:  cache,
	}
}

// thunderResponse is a partial shape of the Thunder API response.
type thunderResponse struct {
	TotalItems int           `json:"totalItems"`
	Items      []thunderItem `json:"items"`
}

// thunderItem is one catalog entry from the Thunder API. JSON tags double as
// the cache round-trip encoding for SearchMedia results.
type thunderItem struct {
	ID               string `json:"id"`
	ReserveID        string `json:"reserveId"`
	Title            string `json:"title"`
	FirstCreatorName string `json:"firstCreatorName"`
	Description      string `json:"description"` // raw HTML, publisher blurb
	IsAvailable      bool   `json:"isAvailable"`
	AvailableCopies  int    `json:"availableCopies"`
	OwnedCopies      int    `json:"ownedCopies"`
	HoldsCount       int    `json:"holdsCount"`
	EstimatedWait    int    `json:"estimatedWaitDays"`
	Covers           struct {
		Cover150 struct {
			Href string `json:"href"`
		} `json:"cover150Wide"`
		Cover300 struct {
			Href string `json:"href"`
		} `json:"cover300Wide"`
	} `json:"covers"`
	Formats []struct {
		ID   string `json:"id"`   // e.g. "ebook-kindle", "ebook-overdrive", "ebook-epub-adobe"
		ISBN string `json:"isbn"` // present on ebook-overdrive/epub-adobe formats, not kindle
	} `json:"formats"`
	DetailedSeries struct {
		SeriesName   string `json:"seriesName"`
		ReadingOrder string `json:"readingOrder"` // numeric string, e.g. "20" or "3.5"
	} `json:"detailedSeries"`
}

func (t *thunderItem) hasKindle() bool {
	for _, f := range t.Formats {
		if f.ID == "ebook-kindle" {
			return true
		}
	}
	return false
}

func (t *thunderItem) coverURL() string {
	if t.Covers.Cover300.Href != "" {
		return t.Covers.Cover300.Href
	}
	return t.Covers.Cover150.Href
}

// bestISBN returns the first ISBN found across this item's formats.
func (t *thunderItem) bestISBN() string {
	for _, f := range t.Formats {
		if f.ISBN != "" {
			return f.ISBN
		}
	}
	return ""
}

// libbyDeepLink builds the Libby app URL for a Thunder media ID.
func libbyDeepLink(libraryKey, mediaID string) string {
	return fmt.Sprintf("https://libbyapp.com/library/%s/everything/page-1/%s", libraryKey, url.PathEscape(mediaID))
}

// CheckAvailability returns availability for a book at a library.
// It searches by ISBN first, falling back to "title author".
// Concurrent checks for the same book+library — two users sharing a shortlink,
// or prewarm overlapping a live search — are coalesced into one upstream call.
func (s *OverDriveService) CheckAvailability(ctx context.Context, book models.Book, libraryKey string) (models.LibraryResult, error) {
	// Build query: prefer ISBN, fall back to title+author
	query := book.BestISBN()
	if query == "" {
		query = fmt.Sprintf("%s %s", book.SearchTitle(), book.Author)
	}

	// Check cache
	var cached models.LibraryResult
	hit, err := s.cache.GetLibrary(libraryKey, query, &cached)
	if err != nil {
		slog.Warn("cache read error", "err", err)
	}
	if hit {
		return cached, nil
	}

	v, err, _ := s.flight.Do(libraryKey+"|"+query, func() (interface{}, error) {
		return s.checkUncached(ctx, book, libraryKey, query)
	})
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("overdrive").Inc()
		return models.LibraryResult{LibraryKey: libraryKey, Status: models.StatusNotFound}, err
	}
	return v.(models.LibraryResult), nil
}

func (s *OverDriveService) checkUncached(ctx context.Context, book models.Book, libraryKey, query string) (models.LibraryResult, error) {
	result := models.LibraryResult{
		LibraryKey: libraryKey,
		Status:     models.StatusNotFound,
	}

	byISBN := query == book.BestISBN() && query != ""

	resp, err := s.fetchThunder(ctx, libraryKey, query)
	if err != nil {
		return result, err
	}

	if resp.TotalItems == 0 || len(resp.Items) == 0 {
		// If ISBN search returned nothing, try title+author
		if byISBN {
			fallback := fmt.Sprintf("%s %s", book.SearchTitle(), book.Author)
			resp, err = s.fetchThunder(ctx, libraryKey, fallback)
			if err != nil {
				return result, err
			}
			query = fallback
			byISBN = false
		}
	}

	// Thunder text search ranks by relevance, not exactness — the top hit for
	// "The Gunslinger Stephen King" can be a series tie-in. Trust position 0
	// only for ISBN queries; otherwise require a title match, and report
	// not-found rather than deep-link the wrong book.
	match := -1
	if len(resp.Items) > 0 {
		if byISBN {
			match = 0
		} else {
			for i, it := range resp.Items {
				if titlesMatch(it.Title, book.SearchTitle()) {
					match = i
					break
				}
			}
		}
	}

	if match >= 0 {
		item := resp.Items[match]
		result.AvailableCopies = item.AvailableCopies
		result.OwnedCopies = item.OwnedCopies
		result.HoldsCount = item.HoldsCount
		result.EstimatedWait = item.EstimatedWait

		// Build the best Libby deep-link we can.
		// Thunder API returns the media ID as "id" (numeric string, e.g. "3994736").
		// "reserveId" is a UUID used for borrowing — "id" is what Libby deep-links use.
		mediaID := item.ID
		if mediaID == "" {
			mediaID = item.ReserveID
		}
		slog.Debug("thunder item", "library", libraryKey, "title", item.Title, "id", item.ID, "reserveId", item.ReserveID)
		if mediaID != "" {
			result.OverDriveURL = libbyDeepLink(libraryKey, mediaID)
		} else {
			// Fallback: Libby title search (always works, lands on search results)
			result.OverDriveURL = fmt.Sprintf("https://libbyapp.com/library/%s/search/query-%s", libraryKey, url.PathEscape(book.SearchTitle()))
		}

		switch {
		case item.IsAvailable:
			result.Status = models.StatusAvailable
		case item.OwnedCopies > 0:
			result.Status = models.StatusWait
		default:
			result.Status = models.StatusUnavailable
		}

		result.HasKindle = item.hasKindle()
	}

	if err := s.cache.SetLibrary(libraryKey, query, result); err != nil {
		slog.Warn("cache write error", "err", err)
	}

	return result, nil
}

// SearchMedia runs a Thunder catalog text search and returns the raw items —
// used by the recommendation engine, which needs series and format details
// rather than a single availability verdict. Results are cached in
// library_cache under a "media:" key prefix (same TTL as availability rows)
// and concurrent identical searches are coalesced.
func (s *OverDriveService) SearchMedia(ctx context.Context, libraryKey, query string) ([]thunderItem, error) {
	cacheKey := "media:" + query

	var cached []thunderItem
	if hit, err := s.cache.GetLibrary(libraryKey, cacheKey, &cached); err != nil {
		slog.Warn("cache read error", "err", err)
	} else if hit {
		return cached, nil
	}

	v, err, _ := s.flight.Do(libraryKey+"|"+cacheKey, func() (interface{}, error) {
		resp, err := s.fetchThunderN(ctx, libraryKey, query, 24)
		if err != nil {
			return nil, err
		}
		if err := s.cache.SetLibrary(libraryKey, cacheKey, resp.Items); err != nil {
			slog.Warn("cache write error", "err", err)
		}
		return resp.Items, nil
	})
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("overdrive").Inc()
		return nil, err
	}
	return v.([]thunderItem), nil
}

// libraryResultFromThunderItem builds a LibraryResult from a raw Thunder item
// — used by the recommendation engine, which works directly with SearchMedia
// results rather than going through CheckAvailability's single-item flow.
func libraryResultFromThunderItem(libraryKey string, item thunderItem) models.LibraryResult {
	r := models.LibraryResult{
		LibraryKey:      libraryKey,
		AvailableCopies: item.AvailableCopies,
		OwnedCopies:     item.OwnedCopies,
		HoldsCount:      item.HoldsCount,
		EstimatedWait:   item.EstimatedWait,
		HasKindle:       item.hasKindle(),
	}
	mediaID := item.ID
	if mediaID == "" {
		mediaID = item.ReserveID
	}
	if mediaID != "" {
		r.OverDriveURL = libbyDeepLink(libraryKey, mediaID)
	}
	switch {
	case item.IsAvailable:
		r.Status = models.StatusAvailable
	case item.OwnedCopies > 0:
		r.Status = models.StatusWait
	default:
		r.Status = models.StatusUnavailable
	}
	return r
}

// titlesMatch reports whether a Thunder search result plausibly is the
// requested book. Titles are compared via normalizeTitle after dropping
// subtitles; each colon-separated segment of the result title is tried, so
// "The Dark Tower I: The Gunslinger" still matches "The Gunslinger".
func titlesMatch(got, want string) bool {
	w := normalizeTitle(stripSubtitle(want))
	if w == "" {
		return false
	}
	for _, seg := range strings.Split(got, ":") {
		if normalizeTitle(seg) == w {
			return true
		}
	}
	return false
}

// stripSubtitle drops everything after the first colon.
func stripSubtitle(s string) string {
	if i := strings.Index(s, ":"); i > 0 {
		return s[:i]
	}
	return s
}

func (s *OverDriveService) fetchThunder(ctx context.Context, libraryKey, query string) (*thunderResponse, error) {
	return s.fetchThunderN(ctx, libraryKey, query, 5)
}

func (s *OverDriveService) fetchThunderN(ctx context.Context, libraryKey, query string, limit int) (*thunderResponse, error) {
	endpoint := fmt.Sprintf(thunderBase, url.PathEscape(libraryKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("format", "ebook-overdrive,ebook-epub-adobe,ebook-kindle")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", "benreadin/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunder API request: %w", err)
	}

	if resp.StatusCode >= 500 {
		// Retry once after a short pause on transient server errors.
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(thunderRetryDelay):
		}
		req2, err2 := http.NewRequestWithContext(ctx, http.MethodGet, req.URL.String(), nil)
		if err2 != nil {
			return nil, err2
		}
		req2.Header.Set("User-Agent", "benreadin/1.0")
		resp, err = s.client.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("thunder API retry: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thunder API returned %d for library %s", resp.StatusCode, libraryKey)
	}

	var result thunderResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode thunder response: %w", err)
	}

	return &result, nil
}
