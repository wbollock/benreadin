package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wbollock/benreadin/internal/models"
	"golang.org/x/sync/semaphore"
)

const (
	openLibraryISBN   = "https://openlibrary.org/isbn/%s.json"
	openLibrarySearch = "https://openlibrary.org/search.json"
	openLibraryCover  = "https://covers.openlibrary.org/b/isbn/%s-L.jpg"
	openLibraryWork   = "https://openlibrary.org%s.json" // %s = work key, e.g. "/works/OL123W"

	// Open Library allows up to ~6 req/sec with a User-Agent set.
	olConcurrency = 6
)

// OpenLibraryService enriches books with ISBN-13 and cover images.
type OpenLibraryService struct {
	client *http.Client
	sem    *semaphore.Weighted
}

func NewOpenLibraryService() *OpenLibraryService {
	return &OpenLibraryService{
		client: newHTTPClient(8*time.Second, olConcurrency),
		sem:    semaphore.NewWeighted(olConcurrency),
	}
}

type olISBNResponse struct {
	ISBN13        []string `json:"isbn_13"`
	ISBN10        []string `json:"isbn_10"`
	Covers        []int    `json:"covers"`
	NumberOfPages int      `json:"number_of_pages"`
	Works         []struct {
		Key string `json:"key"`
	} `json:"works"`
}

// olText handles Open Library's inconsistent "description" shape: a plain
// string on some records, {"type": "/type/text", "value": "..."} on others.
type olText struct {
	Value string
}

func (t *olText) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		t.Value = s
		return nil
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	t.Value = obj.Value
	return nil
}

type olWorkResponse struct {
	Description olText `json:"description"`
}

type olSearchResponse struct {
	Docs []struct {
		Key                 string   `json:"key"` // work key, e.g. "/works/OL123W"
		ISBN                []string `json:"isbn"`
		NumberOfPagesMedian int      `json:"number_of_pages_median"`
	} `json:"docs"`
}

// Enrich fills in missing ISBN-13 for books concurrently.
// It only calls Open Library when a book is missing ISBN-13 — cover images
// from Goodreads are used as-is; Open Library covers are only used as a last resort.
func (s *OpenLibraryService) Enrich(ctx context.Context, books []models.Book) []models.Book {
	enriched := make([]models.Book, len(books))
	copy(enriched, books)

	var wg sync.WaitGroup
	for i := range enriched {
		b := &enriched[i]
		// Skip if we already have everything we'd fetch
		if b.ISBN13 != "" && b.PageCount > 0 {
			continue
		}
		// Nothing to look up and no ISBN at all — skip
		if b.ISBN10 == "" && b.ISBN13 == "" && b.Title == "" {
			continue
		}
		wg.Add(1)
		go func(b *models.Book) {
			defer wg.Done()
			if err := s.sem.Acquire(ctx, 1); err != nil {
				return
			}
			defer s.sem.Release(1)
			if err := s.enrichBook(ctx, b); err != nil {
				slog.Debug("open library enrich failed", "title", b.Title, "err", err)
			}
		}(b)
	}
	wg.Wait()

	return enriched
}

func (s *OpenLibraryService) enrichBook(ctx context.Context, b *models.Book) error {
	// Prefer ISBN-13 lookup if we have one — gets page count directly.
	if b.ISBN13 != "" {
		if err := s.lookupByISBN(ctx, b, b.ISBN13); err == nil {
			return nil
		}
	}
	// Fall back to ISBN-10.
	if b.ISBN10 != "" {
		if err := s.lookupByISBN(ctx, b, b.ISBN10); err == nil {
			return nil
		}
	}
	// Last resort: title+author search (also gets page count median).
	if b.CoverURL == "" || strings.Contains(b.CoverURL, "nophoto") || b.PageCount == 0 {
		return s.lookupBySearch(ctx, b)
	}
	return nil
}

func (s *OpenLibraryService) lookupByISBN(ctx context.Context, b *models.Book, isbn string) error {
	endpoint := fmt.Sprintf(openLibraryISBN, url.PathEscape(isbn))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("open library returned %d", resp.StatusCode)
	}

	var ol olISBNResponse
	if err := json.NewDecoder(resp.Body).Decode(&ol); err != nil {
		return err
	}

	if len(ol.ISBN13) > 0 && b.ISBN13 == "" {
		b.ISBN13 = ol.ISBN13[0]
	}
	if ol.NumberOfPages > 0 && b.PageCount == 0 {
		b.PageCount = ol.NumberOfPages
	}

	// Build cover URL from ISBN if we have one
	if b.CoverURL == "" || strings.Contains(b.CoverURL, "nophoto") {
		coverISBN := b.ISBN13
		if coverISBN == "" {
			coverISBN = isbn
		}
		b.CoverURL = fmt.Sprintf(openLibraryCover, coverISBN)
	}

	// Last-resort description source: editions rarely carry one, but the
	// work they belong to almost always does. Only fires for the minority of
	// books Goodreads (no review) and OverDrive (no catalog match) both missed.
	if b.Description == "" && len(ol.Works) > 0 && ol.Works[0].Key != "" {
		if desc := s.fetchWorkDescription(ctx, ol.Works[0].Key); desc != "" {
			b.Description = truncate(stripHTML(desc), 300)
		}
	}

	return nil
}

// fetchWorkDescription fetches the synopsis for an Open Library work. Best
// effort — errors are swallowed since this only backfills an optional field.
func (s *OpenLibraryService) fetchWorkDescription(ctx context.Context, workKey string) string {
	endpoint := fmt.Sprintf(openLibraryWork, workKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")

	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var work olWorkResponse
	if err := json.NewDecoder(resp.Body).Decode(&work); err != nil {
		return ""
	}
	return work.Description.Value
}

type olRatingResponse struct {
	Docs []struct {
		RatingsAverage float64 `json:"ratings_average"`
		RatingsCount   int     `json:"ratings_count"`
	} `json:"docs"`
}

// FetchRating best-effort backfills a community rating from Open Library's
// own reader ratings. Used only for recommendations — books with no
// Goodreads rating of their own since they aren't on the user's shelf. Never
// overwrites an existing rating (e.g. a real Goodreads average), and the
// pool of Open Library ratings is much smaller than Goodreads', so the
// result is tagged with RatingSource for honest labeling in the UI.
func (s *OpenLibraryService) FetchRating(ctx context.Context, b *models.Book) {
	if b.AverageRating > 0 || b.Title == "" {
		return
	}
	if err := s.sem.Acquire(ctx, 1); err != nil {
		return
	}
	defer s.sem.Release(1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openLibrarySearch, nil)
	if err != nil {
		return
	}
	q := req.URL.Query()
	q.Set("title", b.SearchTitle())
	q.Set("author", b.Author)
	q.Set("limit", "1")
	q.Set("fields", "ratings_average,ratings_count")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")

	resp, err := s.client.Do(req)
	if err != nil {
		slog.Debug("open library rating fetch failed", "title", b.Title, "err", err)
		return
	}
	defer resp.Body.Close()

	var out olRatingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return
	}
	if len(out.Docs) == 0 || out.Docs[0].RatingsAverage <= 0 {
		return
	}
	b.AverageRating = out.Docs[0].RatingsAverage
	b.RatingsCount = out.Docs[0].RatingsCount
	b.RatingSource = "openlibrary"
}

func (s *OpenLibraryService) lookupBySearch(ctx context.Context, b *models.Book) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openLibrarySearch, nil)
	if err != nil {
		return err
	}

	q := req.URL.Query()
	q.Set("title", b.SearchTitle())
	q.Set("author", b.Author)
	q.Set("limit", "1")
	q.Set("fields", "key,isbn,number_of_pages_median")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var ol olSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&ol); err != nil {
		return err
	}

	if len(ol.Docs) == 0 {
		return nil
	}
	doc := ol.Docs[0]

	for _, isbn := range doc.ISBN {
		if len(isbn) == 13 && b.ISBN13 == "" {
			b.ISBN13 = isbn
		}
		if len(isbn) == 10 && b.ISBN10 == "" {
			b.ISBN10 = isbn
		}
	}
	if doc.NumberOfPagesMedian > 0 && b.PageCount == 0 {
		b.PageCount = doc.NumberOfPagesMedian
	}

	if b.CoverURL == "" && b.ISBN13 != "" {
		b.CoverURL = fmt.Sprintf(openLibraryCover, b.ISBN13)
	}

	// Last-resort description source — see the identical comment in
	// lookupByISBN. Reached here for books with no ISBN at all.
	if b.Description == "" && doc.Key != "" {
		if desc := s.fetchWorkDescription(ctx, doc.Key); desc != "" {
			b.Description = truncate(stripHTML(desc), 300)
		}
	}

	return nil
}
