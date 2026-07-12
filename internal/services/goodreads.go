package services

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
)

var (
	reHTMLTag    = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`\s+`)
)

const goodreadsRSSBase = "https://www.goodreads.com/review/list_rss/%s"

// GoodreadsStatusError is a non-200 response from the Goodreads RSS feed. Its
// message is written for the end user — the search UI shows it verbatim.
type GoodreadsStatusError struct {
	Code int
}

func (e *GoodreadsStatusError) Error() string {
	switch {
	case e.Code == http.StatusNotFound:
		return "Goodreads couldn't find that user or shelf. Double-check the ID or URL — and if it's yours, make sure the profile is public (Goodreads → Account Settings → Privacy → \"anyone\" can view my profile)."
	case e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden:
		return "That Goodreads profile looks private. Make it public under Goodreads → Account Settings → Privacy, then try again."
	case e.Code == http.StatusTooManyRequests:
		return "Goodreads is rate-limiting requests right now — wait a minute and try again."
	case e.Code >= 500:
		return "Goodreads is having trouble right now — try again in a few minutes."
	default:
		return fmt.Sprintf("Goodreads returned an unexpected error (HTTP %d).", e.Code)
	}
}

// GoodreadsService fetches and parses a Goodreads shelf RSS feed.
type GoodreadsService struct {
	client *http.Client
	cache  *CacheService // nil disables shelf caching (tests)
	flight singleflight.Group
}

func NewGoodreadsService(cache *CacheService) *GoodreadsService {
	return &GoodreadsService{
		// 4 matches FetchShelf's page-fetch concurrency.
		client: newHTTPClient(20*time.Second, 4),
		cache:  cache,
	}
}

// grFeed is the top-level RSS envelope.
type grFeed struct {
	Items []grItem `xml:"channel>item"`
}

// grItem maps the fields Goodreads puts directly on each <item>.
type grItem struct {
	GUID              string `xml:"guid"`
	Title             string `xml:"title"`
	BookID            string `xml:"book_id"`
	AuthorName        string `xml:"author_name"`
	ISBN              string `xml:"isbn"`
	BookLargeImageURL string `xml:"book_large_image_url"`
	BookImageURL      string `xml:"book_image_url"`
	Description       string `xml:"description"`
	AverageRating     string `xml:"average_rating"`
	UserRating        string `xml:"user_rating"`
}

// FetchShelf returns all books on a Goodreads shelf. Results are cached
// (shelf_cache table, short TTL) so repeat searches — a shared shortlink, a
// page reload, the prewarm scheduler — skip the RSS round trips; refresh
// bypasses the cache read. Concurrent fetches of the same shelf are coalesced
// into a single upstream request via singleflight.
func (s *GoodreadsService) FetchShelf(ctx context.Context, userID, shelf string, refresh bool) ([]models.Book, error) {
	cacheKey := userID + "|" + shelf
	if !refresh && s.cache != nil {
		var books []models.Book
		if hit, err := s.cache.GetShelf(cacheKey, &books); err != nil {
			slog.Warn("shelf cache read failed", "err", err)
		} else if hit {
			slog.Debug("shelf cache hit", "user", userID, "shelf", shelf)
			return books, nil
		}
	}

	// Coalesce concurrent fetches. Waiters share the leader's result — and its
	// error if the leader's context is canceled, which is acceptable at this
	// scale (the client just retries).
	v, err, _ := s.flight.Do(cacheKey, func() (interface{}, error) {
		timer := prometheus.NewTimer(metrics.ShelfFetchDuration)
		books, err := s.fetchShelfPages(ctx, userID, shelf)
		timer.ObserveDuration()
		if err != nil {
			metrics.UpstreamErrorsTotal.WithLabelValues("goodreads").Inc()
			return nil, err
		}
		if s.cache != nil {
			if err := s.cache.SetShelf(cacheKey, books); err != nil {
				slog.Warn("shelf cache write failed", "err", err)
			}
		}
		return books, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]models.Book), nil
}

// fetchShelfPages paginates through a Goodreads RSS shelf. Page 1 is fetched
// first; if it is full (200 items), subsequent pages are fetched concurrently
// (up to 4 at a time, capped at page 5 = 1000 books).
func (s *GoodreadsService) fetchShelfPages(ctx context.Context, userID, shelf string) ([]models.Book, error) {
	// Fetch page 1 first to determine pagination needs.
	page1, err := s.fetchPage(ctx, buildFeedURL(userID, shelf, 1))
	if err != nil {
		var statusErr *GoodreadsStatusError
		if errors.As(err, &statusErr) {
			return nil, statusErr // already worded for the end user
		}
		return nil, fmt.Errorf("fetch goodreads page 1: %w", err)
	}
	if len(page1) == 0 {
		return nil, nil
	}

	books := itemsToBooks(page1)

	// If page 1 was full, fetch remaining pages concurrently.
	const maxPage = 5
	const concurrency = 4
	if len(page1) == 200 {
		type pageResult struct {
			page  int
			items []grItem
		}
		pageCh := make(chan pageResult, maxPage)

		g, gCtx := errgroup.WithContext(ctx)
		sem := make(chan struct{}, concurrency)
		for p := 2; p <= maxPage; p++ {
			p := p
			sem <- struct{}{}
			g.Go(func() error {
				defer func() { <-sem }()
				items, err := s.fetchPage(gCtx, buildFeedURL(userID, shelf, p))
				if err != nil {
					slog.Warn("goodreads page fetch failed", "page", p, "err", err)
					return nil // non-fatal; we return what we have
				}
				pageCh <- pageResult{page: p, items: items}
				return nil
			})
		}

		go func() {
			_ = g.Wait()
			close(pageCh)
		}()

		// Collect pages into a sorted slice.
		pageMap := make(map[int][]grItem)
		for r := range pageCh {
			pageMap[r.page] = r.items
		}

		// Append in order; stop when a page is empty (last page).
		for p := 2; p <= maxPage; p++ {
			items := pageMap[p]
			if len(items) == 0 {
				break
			}
			books = append(books, itemsToBooks(items)...)
			if len(items) < 200 {
				break
			}
		}
	}

	// Deduplicate by goodreads ID.
	seen := make(map[string]bool, len(books))
	deduped := books[:0]
	for _, b := range books {
		if !seen[b.GoodreadsID] {
			seen[b.GoodreadsID] = true
			deduped = append(deduped, b)
		}
	}

	slog.Info("goodreads fetch complete", "total_books", len(deduped))
	return deduped, nil
}

// itemsToBooks converts a slice of grItem to models.Book, upscaling cover URLs.
func itemsToBooks(items []grItem) []models.Book {
	out := make([]models.Book, 0, len(items))
	for _, item := range items {
		out = append(out, itemToBook(item))
	}
	return out
}

func (s *GoodreadsService) fetchPage(ctx context.Context, feedURL string) ([]grItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &GoodreadsStatusError{Code: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var feed grFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse rss: %w", err)
	}

	return feed.Items, nil
}

func buildFeedURL(userID, shelf string, page int) string {
	u := fmt.Sprintf(goodreadsRSSBase, url.PathEscape(userID))
	return fmt.Sprintf("%s?shelf=%s&per_page=200&page=%d", u, url.QueryEscape(shelf), page)
}

// stripHTML removes HTML tags and collapses whitespace, returning plain text.
func stripHTML(s string) string {
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(s)
	return strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
}

// truncate cuts s to at most maxRunes runes, appending "…" if trimmed.
func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	cut := maxRunes
	for cut > 0 && runes[cut-1] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = maxRunes
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
}

func extractReview(rawHTML string) string {
	stripped := stripHTML(rawHTML)
	if idx := strings.Index(stripped, "review: "); idx >= 0 {
		after := strings.TrimSpace(stripped[idx+8:])
		if len(after) > 5 {
			return after
		}
	}
	return ""
}

// upscaleCoverURL bumps low-res Goodreads thumbnails to a larger size and
// forces HTTPS.
func upscaleCoverURL(u string) string {
	if u == "" {
		return ""
	}
	// Force HTTPS.
	u = strings.Replace(u, "http://", "https://", 1)
	// Goodreads low-res pattern: _SX98_ → _SY475_ (larger cached size).
	u = strings.Replace(u, "_SX98_", "_SY475_", 1)
	return u
}

func itemToBook(item grItem) models.Book {
	b := models.Book{
		Author: strings.TrimSpace(item.AuthorName),
		ISBN10: strings.TrimSpace(item.ISBN),
		Title:  strings.TrimSpace(item.Title),
	}

	if raw := extractReview(item.Description); raw != "" {
		b.Description = truncate(raw, 300)
	}

	// Prefer large image, fall back to standard image, then upscale.
	if u := strings.TrimSpace(item.BookLargeImageURL); u != "" {
		b.CoverURL = upscaleCoverURL(u)
	} else if u := strings.TrimSpace(item.BookImageURL); u != "" {
		b.CoverURL = upscaleCoverURL(u)
	}

	// Book ID from dedicated field — use this as the canonical cache key.
	// Do NOT fall back to the review ID: caching by review ID would create
	// duplicate cache entries for the same book across different users.
	if id := strings.TrimSpace(item.BookID); id != "" {
		b.GoodreadsID = id
	}

	if v, err := strconv.ParseFloat(strings.TrimSpace(item.AverageRating), 64); err == nil {
		b.AverageRating = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(item.UserRating)); err == nil {
		b.UserRating = v
	}

	return b
}
