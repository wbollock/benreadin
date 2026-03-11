package services

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wbollock/shelfprice/internal/models"
)

var (
	reHTMLTag    = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`\s+`)
)

const goodreadsRSSBase = "https://www.goodreads.com/review/list_rss/%s"

// GoodreadsService fetches and parses a Goodreads shelf RSS feed.
type GoodreadsService struct {
	client *http.Client
}

func NewGoodreadsService() *GoodreadsService {
	return &GoodreadsService{
		client: &http.Client{Timeout: 20 * time.Second},
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

// FetchShelf paginates through a Goodreads RSS shelf and returns all books.
func (s *GoodreadsService) FetchShelf(ctx context.Context, userID, shelf string) ([]models.Book, error) {
	var books []models.Book
	seen := make(map[string]bool)

	for page := 1; ; page++ {
		feedURL := buildFeedURL(userID, shelf, page)
		slog.Info("fetching goodreads page", "page", page, "url", feedURL)

		items, err := s.fetchPage(ctx, feedURL)
		if err != nil {
			return nil, fmt.Errorf("fetch goodreads page %d: %w", page, err)
		}

		if len(items) == 0 {
			break
		}

		for _, item := range items {
			b := itemToBook(item)
			if seen[b.GoodreadsID] {
				continue
			}
			seen[b.GoodreadsID] = true
			books = append(books, b)
		}

		// Goodreads returns up to 200 items/page; fewer means last page
		if len(items) < 200 {
			break
		}
	}

	slog.Info("goodreads fetch complete", "total_books", len(books))
	return books, nil
}

func (s *GoodreadsService) fetchPage(ctx context.Context, feedURL string) ([]grItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "shelfprice/1.0 (+https://github.com/wbollock/shelfprice)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("goodreads returned %d", resp.StatusCode)
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
// It also HTML-entity-decodes the most common entities Goodreads uses.
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
	// Walk back to the last word boundary so we don't cut mid-word.
	cut := maxRunes
	for cut > 0 && runes[cut-1] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = maxRunes
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
}

func itemToBook(item grItem) models.Book {
	b := models.Book{
		Author: strings.TrimSpace(item.AuthorName),
		ISBN10: strings.TrimSpace(item.ISBN),
		Title:  strings.TrimSpace(item.Title),
	}

	if raw := stripHTML(item.Description); raw != "" {
		b.Description = truncate(raw, 300)
	}

	// Prefer large image, fall back to standard image
	if u := strings.TrimSpace(item.BookLargeImageURL); u != "" {
		b.CoverURL = u
	} else if u := strings.TrimSpace(item.BookImageURL); u != "" {
		b.CoverURL = u
	}

	// Book ID from dedicated field
	if id := strings.TrimSpace(item.BookID); id != "" {
		b.GoodreadsID = id
	}

	// Fall back: parse review ID out of GUID
	// GUID looks like: https://www.goodreads.com/review/show/8426975079?utm_medium=api&utm_source=rss
	if b.GoodreadsID == "" && item.GUID != "" {
		raw := item.GUID
		if idx := strings.Index(raw, "?"); idx != -1 {
			raw = raw[:idx]
		}
		parts := strings.Split(raw, "/")
		b.GoodreadsID = parts[len(parts)-1]
	}

	if v, err := strconv.ParseFloat(strings.TrimSpace(item.AverageRating), 64); err == nil {
		b.AverageRating = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(item.UserRating)); err == nil {
		b.UserRating = v
	}

	return b
}
