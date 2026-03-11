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

	"github.com/wbollock/shelfprice/internal/models"
	"golang.org/x/sync/semaphore"
)

const (
	olWorksBySubject = "https://openlibrary.org/subjects/%s.json?limit=8&ebooks=false"
	olSearchByTitle  = "https://openlibrary.org/search.json"

	// Maximum recommendations to return.
	maxRecs = 10
	// Max concurrent Open Library calls for recommendations.
	recOLConcurrency = 3
	// Max concurrent Libby availability checks for rec candidates.
	recLibbyConcurrency = 4
)

// recCandidate is a book surfaced from Open Library subject search.
type recCandidate struct {
	title     string
	author    string
	isbn13    string
	coverURL  string
	becauseOf string
}

// recResult pairs a resolved recommendation with a success flag.
type recResult struct {
	rec models.Recommendation
	ok  bool
}

// RecommendationService finds books similar to a shelf that are available on Libby.
type RecommendationService struct {
	client    *http.Client
	overdrive *OverDriveService
	sem       *semaphore.Weighted
}

func NewRecommendationService(overdrive *OverDriveService) *RecommendationService {
	return &RecommendationService{
		client:    &http.Client{Timeout: 10 * time.Second},
		overdrive: overdrive,
		sem:       semaphore.NewWeighted(recOLConcurrency),
	}
}

// olSearchResult is the Open Library search API response shape (fields we care about).
type olSearchResult struct {
	Docs []struct {
		Title       string   `json:"title"`
		AuthorNames []string `json:"author_name"`
		ISBN        []string `json:"isbn"`
		CoverI      int      `json:"cover_i"`
		Subject     []string `json:"subject"`
	} `json:"docs"`
}

// olSubjectResult is the Open Library subjects endpoint response shape.
type olSubjectResult struct {
	Works []struct {
		Title   string `json:"title"`
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
		CoverID int `json:"cover_id"`
	} `json:"works"`
}

// FindRecommendations takes the enriched shelf books and the chosen library keys,
// then returns up to maxRecs books that are available to borrow right now.
func (s *RecommendationService) FindRecommendations(
	ctx context.Context,
	books []models.Book,
	libraryKeys []string,
) []models.Recommendation {
	if len(books) == 0 || len(libraryKeys) == 0 {
		return nil
	}

	// Build a set of shelf titles (lowercased) so we can exclude them from recs.
	shelfTitles := make(map[string]bool, len(books))
	for _, b := range books {
		shelfTitles[strings.ToLower(b.Title)] = true
	}

	candCh := make(chan recCandidate, 64)
	var wg sync.WaitGroup

	// Sample up to 5 books spread evenly across the shelf to keep API calls reasonable.
	sample := sampleBooks(books, 5)

	for _, b := range sample {
		wg.Add(1)
		go func(b models.Book) {
			defer wg.Done()
			if err := s.sem.Acquire(ctx, 1); err != nil {
				return
			}
			defer s.sem.Release(1)

			candidates, err := s.fetchSimilar(ctx, b)
			if err != nil {
				slog.Debug("recommendations: fetchSimilar failed", "title", b.Title, "err", err)
				return
			}
			for _, c := range candidates {
				if !shelfTitles[strings.ToLower(c.title)] {
					c.becauseOf = b.Title
					candCh <- c
				}
			}
		}(b)
	}

	go func() {
		wg.Wait()
		close(candCh)
	}()

	// Deduplicate candidates by title.
	seen := make(map[string]bool)
	var deduped []recCandidate
	for c := range candCh {
		key := strings.ToLower(c.title)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, c)
		}
	}

	if len(deduped) == 0 {
		return nil
	}

	// Check Libby availability for each candidate, keeping only available ones.
	libbySem := semaphore.NewWeighted(recLibbyConcurrency)
	resultCh := make(chan recResult, len(deduped))
	var wg2 sync.WaitGroup

	for _, c := range deduped {
		wg2.Add(1)
		go func(c recCandidate) {
			defer wg2.Done()
			if err := libbySem.Acquire(ctx, 1); err != nil {
				return
			}
			defer libbySem.Release(1)

			candidate := models.Book{
				Title:  c.title,
				Author: c.author,
				ISBN13: c.isbn13,
			}

			var libResults []models.LibraryResult
			anyAvailable := false
			for _, key := range libraryKeys {
				lr, err := s.overdrive.CheckAvailability(ctx, candidate, key)
				if err != nil {
					continue
				}
				libResults = append(libResults, lr)
				if lr.Status == models.StatusAvailable {
					anyAvailable = true
				}
			}

			if !anyAvailable {
				resultCh <- recResult{ok: false}
				return
			}

			resultCh <- recResult{
				ok: true,
				rec: models.Recommendation{
					Title:          c.title,
					Author:         c.author,
					CoverURL:       c.coverURL,
					ISBN13:         c.isbn13,
					LibraryResults: libResults,
					BecauseOfTitle: c.becauseOf,
				},
			}
		}(c)
	}

	wg2.Wait()
	close(resultCh)

	var recs []models.Recommendation
	for r := range resultCh {
		if r.ok {
			recs = append(recs, r.rec)
			if len(recs) >= maxRecs {
				break
			}
		}
	}

	return recs
}

// fetchSimilar returns candidate books from Open Library that share subjects
// with the given book.
func (s *RecommendationService) fetchSimilar(ctx context.Context, b models.Book) ([]recCandidate, error) {
	// Search Open Library for the book to get its subjects.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, olSearchByTitle, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("title", b.Title)
	q.Set("author", b.Author)
	q.Set("limit", "1")
	q.Set("fields", "subject,cover_i,isbn,title,author_name")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "shelfprice/1.0 (+https://github.com/wbollock/shelfprice)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var sr olSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	if len(sr.Docs) == 0 || len(sr.Docs[0].Subject) == 0 {
		return nil, nil
	}

	subject := pickSubject(sr.Docs[0].Subject)
	if subject == "" {
		return nil, nil
	}

	slog.Debug("recommendations: subject search", "book", b.Title, "subject", subject)

	// Fetch works for that subject.
	subjectSlug := strings.ToLower(strings.ReplaceAll(subject, " ", "_"))
	subjectURL := fmt.Sprintf(olWorksBySubject, url.PathEscape(subjectSlug))
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, subjectURL, nil)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("User-Agent", "shelfprice/1.0 (+https://github.com/wbollock/shelfprice)")

	resp2, err := s.client.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()

	var subj olSubjectResult
	if err := json.NewDecoder(resp2.Body).Decode(&subj); err != nil {
		return nil, err
	}

	var out []recCandidate
	for _, w := range subj.Works {
		if strings.EqualFold(w.Title, b.Title) {
			continue
		}
		author := ""
		if len(w.Authors) > 0 {
			author = w.Authors[0].Name
		}
		cover := ""
		if w.CoverID > 0 {
			cover = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", w.CoverID)
		}
		// ISBN is not available from the subjects endpoint; OverDrive will fall back
		// to a title+author search automatically.
		out = append(out, recCandidate{
			title:    w.Title,
			author:   author,
			coverURL: cover,
		})
	}
	return out, nil
}

// sampleBooks returns up to n books spread evenly across the slice.
func sampleBooks(books []models.Book, n int) []models.Book {
	if len(books) <= n {
		return books
	}
	step := len(books) / n
	out := make([]models.Book, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, books[i*step])
	}
	return out
}

// pickSubject chooses the most useful subject from an Open Library subject list.
// It prefers subjects in the 2-5 word range and skips very generic ones.
var genericSubjects = map[string]bool{
	"fiction": true, "nonfiction": true, "literature": true,
	"novels": true, "short stories": true, "essays": true,
	"biography": true, "history": true, "poetry": true,
	"accessible book": true, "protected daisy": true,
	"in library": true, "overdrive": true,
}

func pickSubject(subjects []string) string {
	for _, s := range subjects {
		low := strings.ToLower(s)
		if genericSubjects[low] {
			continue
		}
		words := strings.Fields(s)
		if len(words) >= 2 && len(words) <= 5 {
			return s
		}
	}
	// Fall back to first non-generic subject of any length.
	for _, s := range subjects {
		if !genericSubjects[strings.ToLower(s)] {
			return s
		}
	}
	return ""
}
