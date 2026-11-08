package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/wbollock/benreadin/internal/db"
	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
)

const (
	hardcoverAPIURL = "https://api.hardcover.app/v1/graphql"
	// hardcoverPageSize is the user_books page size. The API returned 1000+
	// rows in a single response in testing; 500 keeps responses modest while
	// covering almost every shelf in one request.
	hardcoverPageSize = 500
	hardcoverMaxPages = 10 // 5000 books
	// hardcoverStaleMaxAge is how old a cached shelf may be and still be
	// served when Hardcover is down or rate-limiting us.
	hardcoverStaleMaxAge = db.HardcoverShelfRetentionSeconds
	// hardcoverReqPerMin stays under Hardcover's ~60 req/min per-token limit,
	// which every visitor (and the prewarm loop) shares.
	hardcoverReqPerMin = 50
	// HardcoverIDPrefix namespaces Hardcover book IDs in Book.GoodreadsID (the
	// app-wide book cache key / DOM id) so they can never collide with numeric
	// Goodreads IDs.
	HardcoverIDPrefix = "hc-"
)

// hardcoverStatusIDs maps benreadin shelf names (Goodreads-style, which the
// shelf picker sends) to Hardcover user_book status IDs.
var hardcoverStatusIDs = map[string]int{
	"to-read":           1,
	"want-to-read":      1,
	"currently-reading": 2,
	"read":              3,
	"did-not-finish":    5,
}

// HardcoverShelfName normalizes a Hardcover URL shelf segment to the
// Goodreads-style name used throughout the app ("want-to-read" → "to-read").
func HardcoverShelfName(s string) string {
	if s == "want-to-read" {
		return "to-read"
	}
	return s
}

// HardcoverError is a failed Hardcover fetch. Its message is written for the
// end user — the search UI shows it verbatim.
type HardcoverError struct {
	Kind string // "not_found" | "auth" | "rate_limited" | "unavailable" | "bad_shelf" | "unconfigured" | "other"
	Msg  string // detail for logs (not shown to users)
}

func (e *HardcoverError) Error() string {
	switch e.Kind {
	case "not_found":
		return "Hardcover couldn't find that user. Double-check the username (hardcover.app/@username)."
	case "auth":
		return "benreadin's Hardcover API token was rejected — it may have expired. The site owner needs to refresh it."
	case "rate_limited":
		return "Hardcover is rate-limiting requests right now — wait a minute and try again."
	case "unavailable":
		return "Hardcover is having trouble right now — try again in a few minutes."
	case "bad_shelf":
		return "Hardcover only has the built-in shelves: to-read, currently-reading, read, did-not-finish."
	case "unconfigured":
		return "Hardcover support isn't configured on this server (no API token)."
	default:
		return "Hardcover returned an unexpected error — try again in a bit."
	}
}

// HardcoverService fetches a user's books by status from the Hardcover
// GraphQL API. One server-side token reads any user's public library.
type HardcoverService struct {
	token    string
	endpoint string
	client   *http.Client
	cache    *CacheService // nil disables shelf caching (tests)
	ttl      int64         // fresh-cache TTL in seconds for Hardcover shelves
	limiter  *rate.Limiter
	flight   singleflight.Group
}

// NewHardcoverService builds the client. ttlSeconds is how long a fetched
// shelf is served from cache before refetching (the results page's Refresh
// button bypasses it); <= 0 uses the cache's default shelf TTL.
func NewHardcoverService(token string, cache *CacheService, ttlSeconds int64) *HardcoverService {
	return &HardcoverService{
		token:    strings.TrimSpace(token),
		endpoint: hardcoverAPIURL,
		client:   newHTTPClient(20*time.Second, 2),
		cache:    cache,
		ttl:      ttlSeconds,
		limiter:  rate.NewLimiter(rate.Every(time.Minute/hardcoverReqPerMin), 5),
	}
}

// Enabled reports whether an API token is configured.
func (s *HardcoverService) Enabled() bool { return s != nil && s.token != "" }

// FetchShelf returns all books with the given status for a Hardcover user.
// username has no leading "@". Caching and request coalescing mirror
// GoodreadsService.FetchShelf; the cache key is "@user|shelf", which can't
// collide with numeric Goodreads IDs.
func (s *HardcoverService) FetchShelf(ctx context.Context, username, shelf string, refresh bool) ([]models.Book, error) {
	if !s.Enabled() {
		return nil, &HardcoverError{Kind: "unconfigured"}
	}
	username = strings.ToLower(strings.TrimPrefix(username, "@"))
	status, ok := hardcoverStatusIDs[shelf]
	if !ok {
		return nil, &HardcoverError{Kind: "bad_shelf", Msg: shelf}
	}

	cacheKey := "@" + username + "|" + HardcoverShelfName(shelf)
	if !refresh && s.cache != nil {
		var books []models.Book
		var hit bool
		var err error
		if s.ttl > 0 {
			hit, err = s.cache.GetShelfMaxAge(cacheKey, s.ttl, &books)
		} else {
			hit, err = s.cache.GetShelf(cacheKey, &books)
		}
		if err != nil {
			slog.Warn("shelf cache read failed", "err", err)
		} else if hit {
			slog.Debug("shelf cache hit", "user", "@"+username, "shelf", shelf)
			return books, nil
		}
	}

	v, err, _ := s.flight.Do(cacheKey, func() (interface{}, error) {
		timer := prometheus.NewTimer(metrics.ShelfFetchDuration)
		books, err := s.fetchAll(ctx, username, status)
		timer.ObserveDuration()
		if err != nil {
			metrics.UpstreamErrorsTotal.WithLabelValues("hardcover").Inc()
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
		if books, ok := s.staleShelf(cacheKey, err); ok {
			return books, nil
		}
		return nil, err
	}
	return v.([]models.Book), nil
}

// staleShelf serves a cached shelf up to a week old when the fetch failed for
// a transient reason (rate limit, outage, network), so a Hardcover hiccup
// degrades to slightly old data instead of an error page. Definitive answers
// (unknown user, bad shelf, rejected token) are never masked.
func (s *HardcoverService) staleShelf(cacheKey string, fetchErr error) ([]models.Book, bool) {
	if s.cache == nil {
		return nil, false
	}
	var hcErr *HardcoverError
	if errors.As(fetchErr, &hcErr) {
		switch hcErr.Kind {
		case "not_found", "bad_shelf", "auth", "unconfigured":
			return nil, false
		}
	}
	var books []models.Book
	hit, err := s.cache.GetShelfMaxAge(cacheKey, hardcoverStaleMaxAge, &books)
	if err != nil || !hit {
		return nil, false
	}
	slog.Warn("hardcover fetch failed; serving stale cached shelf", "key", cacheKey, "err", fetchErr)
	return books, true
}

const hardcoverShelfQuery = `query Shelf($u: citext!, $s: Int!, $l: Int!, $o: Int!) {
  users(where: {username: {_eq: $u}}, limit: 1) { id }
  user_books(
    where: {user: {username: {_eq: $u}}, status_id: {_eq: $s}}
    limit: $l, offset: $o, order_by: {id: asc}
  ) {
    date_added
    rating
    edition { isbn_10 isbn_13 pages image { url } }
    book {
      id slug title description pages rating ratings_count
      image { url }
      contributions { contribution author { name } }
      book_series(order_by: {featured: desc}) { position featured series { name } }
    }
  }
}`

type hcImage struct {
	URL string `json:"url"`
}

type hcUserBook struct {
	DateAdded string   `json:"date_added"`
	Rating    *float64 `json:"rating"`
	Edition   *struct {
		ISBN10 string   `json:"isbn_10"`
		ISBN13 string   `json:"isbn_13"`
		Pages  int      `json:"pages"`
		Image  *hcImage `json:"image"`
	} `json:"edition"`
	Book struct {
		ID            int      `json:"id"`
		Slug          string   `json:"slug"`
		Title         string   `json:"title"`
		Description   string   `json:"description"`
		Pages         int      `json:"pages"`
		Rating        float64  `json:"rating"`
		RatingsCount  int      `json:"ratings_count"`
		Image         *hcImage `json:"image"`
		Contributions []struct {
			Contribution *string `json:"contribution"`
			Author       struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"contributions"`
		BookSeries []struct {
			Position *float64 `json:"position"`
			Featured bool     `json:"featured"`
			Series   *struct {
				Name string `json:"name"`
			} `json:"series"`
		} `json:"book_series"`
	} `json:"book"`
}

type hcResponse struct {
	Data struct {
		Users     []struct{ ID int } `json:"users"`
		UserBooks []hcUserBook       `json:"user_books"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	// Auth failures come back as {"error": "...", "error_description": "..."}.
	Error     string `json:"error"`
	ErrorDesc string `json:"error_description"`
}

func (s *HardcoverService) fetchAll(ctx context.Context, username string, status int) ([]models.Book, error) {
	var books []models.Book
	seen := make(map[string]bool)
	for page := 0; page < hardcoverMaxPages; page++ {
		resp, err := s.query(ctx, username, status, page*hardcoverPageSize)
		if err != nil {
			return nil, err
		}
		if page == 0 && len(resp.Data.Users) == 0 {
			return nil, &HardcoverError{Kind: "not_found", Msg: username}
		}
		for _, ub := range resp.Data.UserBooks {
			b := hardcoverToBook(ub)
			if seen[b.GoodreadsID] {
				continue
			}
			seen[b.GoodreadsID] = true
			books = append(books, b)
		}
		if len(resp.Data.UserBooks) < hardcoverPageSize {
			break
		}
	}
	slog.Info("hardcover fetch complete", "user", "@"+username, "status", status, "total_books", len(books))
	return books, nil
}

func (s *HardcoverService) query(ctx context.Context, username string, status, offset int) (*hcResponse, error) {
	if s.limiter != nil {
		// Queue rather than fail when over budget; a canceled request (user
		// navigated away) stops waiting.
		if err := s.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(map[string]any{
		"query": hardcoverShelfQuery,
		"variables": map[string]any{
			"u": username, "s": status, "l": hardcoverPageSize, "o": offset,
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "benreadin/1.0 (+https://github.com/wbollock/benreadin)")
	// Tokens are copied from the settings page either bare or with the
	// "Bearer " prefix; accept both.
	req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(s.token, "Bearer "))

	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hardcover request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		slog.Error("hardcover auth failed — check HARDCOVER_API_TOKEN", "status", res.StatusCode, "body", truncate(string(raw), 200))
		return nil, &HardcoverError{Kind: "auth"}
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, &HardcoverError{Kind: "rate_limited"}
	case res.StatusCode >= 500:
		return nil, &HardcoverError{Kind: "unavailable", Msg: strconv.Itoa(res.StatusCode)}
	case res.StatusCode != http.StatusOK:
		return nil, &HardcoverError{Kind: "other", Msg: fmt.Sprintf("HTTP %d: %s", res.StatusCode, truncate(string(raw), 200))}
	}

	var out hcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse hardcover response: %w", err)
	}
	if out.Error != "" {
		slog.Error("hardcover auth failed — check HARDCOVER_API_TOKEN", "error", out.Error, "desc", out.ErrorDesc)
		return nil, &HardcoverError{Kind: "auth", Msg: out.ErrorDesc}
	}
	if len(out.Errors) > 0 {
		msg := out.Errors[0].Message
		slog.Warn("hardcover graphql error", "err", msg)
		if strings.Contains(strings.ToLower(msg), "jwt") || strings.Contains(strings.ToLower(msg), "token") {
			return nil, &HardcoverError{Kind: "auth", Msg: msg}
		}
		return nil, &HardcoverError{Kind: "other", Msg: msg}
	}
	return &out, nil
}

// hardcoverAuthor picks the primary author: the first credit whose role is
// empty or "Author", else the first credit of any role.
func hardcoverAuthor(ub hcUserBook) string {
	var fallback string
	for _, c := range ub.Book.Contributions {
		name := strings.TrimSpace(reWhitespace.ReplaceAllString(c.Author.Name, " "))
		if name == "" {
			continue
		}
		if c.Contribution == nil || *c.Contribution == "" || *c.Contribution == "Author" {
			return name
		}
		if fallback == "" {
			fallback = name
		}
	}
	return fallback
}

func hardcoverToBook(ub hcUserBook) models.Book {
	b := models.Book{
		GoodreadsID:   HardcoverIDPrefix + strconv.Itoa(ub.Book.ID),
		Source:        "hardcover",
		Title:         strings.TrimSpace(ub.Book.Title),
		Author:        hardcoverAuthor(ub),
		AverageRating: math.Round(ub.Book.Rating*100) / 100,
		PageCount:     ub.Book.Pages,
	}
	if ub.Book.Slug != "" {
		b.BookURL = "https://hardcover.app/books/" + ub.Book.Slug
	}
	if d := stripHTML(ub.Book.Description); d != "" {
		b.Description = truncate(d, 300)
	}
	if ub.Rating != nil && *ub.Rating > 0 {
		b.UserRating = int(math.Round(*ub.Rating))
	}
	if e := ub.Edition; e != nil {
		b.ISBN10 = strings.TrimSpace(e.ISBN10)
		b.ISBN13 = strings.TrimSpace(e.ISBN13)
		if e.Pages > 0 {
			b.PageCount = e.Pages
		}
		if e.Image != nil && e.Image.URL != "" {
			b.CoverURL = e.Image.URL
		}
	}
	if b.CoverURL == "" && ub.Book.Image != nil {
		b.CoverURL = ub.Book.Image.URL
	}
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(ub.DateAdded)); err == nil {
		b.DateAddedUnix = t.Unix()
	}
	b.SeriesName, b.SeriesPosition = hardcoverSeries(ub)
	return b
}

// hardcoverSeries picks the book's main series entry for the "next in series"
// recommendations: the featured series (e.g. Stormlight Archive rather than
// the Cosmere umbrella) when it has a whole-number position, else the first
// series that does. Novellas at fractional positions (2.5) and unnumbered
// collections are skipped — there's no "next" entry to recommend.
func hardcoverSeries(ub hcUserBook) (string, int) {
	for _, s := range ub.Book.BookSeries { // ordered featured-first
		if s.Series == nil || s.Position == nil {
			continue
		}
		p := *s.Position
		name := strings.TrimSpace(s.Series.Name)
		if name != "" && p >= 1 && p == math.Trunc(p) {
			return name, int(p)
		}
	}
	return "", 0
}

// ShelfFetcher fetches a shelf's books. userID is either a numeric Goodreads
// ID or "@username" for Hardcover.
type ShelfFetcher interface {
	FetchShelf(ctx context.Context, userID, shelf string, refresh bool) ([]models.Book, error)
}

// ShelfRouter dispatches FetchShelf to Hardcover for "@username" IDs and to
// Goodreads otherwise, so callers (search, prewarm, recommendations) and
// stored IDs (recent searches, PREWARM_SEEDS, shortlinks) need no source field.
type ShelfRouter struct {
	Goodreads *GoodreadsService
	Hardcover *HardcoverService
}

func (r *ShelfRouter) FetchShelf(ctx context.Context, userID, shelf string, refresh bool) ([]models.Book, error) {
	if IsHardcoverUser(userID) {
		return r.Hardcover.FetchShelf(ctx, userID, shelf, refresh)
	}
	return r.Goodreads.FetchShelf(ctx, userID, shelf, refresh)
}

// IsHardcoverUser reports whether a parsed userID refers to Hardcover.
func IsHardcoverUser(userID string) bool { return strings.HasPrefix(userID, "@") }
