package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/wbollock/shelfprice/internal/models"
)

const thunderBase = "https://thunder.api.overdrive.com/v2/libraries/%s/media"

// OverDriveService queries the OverDrive Thunder API for library availability.
type OverDriveService struct {
	client *http.Client
	cache  *CacheService
}

func NewOverDriveService(cache *CacheService) *OverDriveService {
	return &OverDriveService{
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  cache,
	}
}

// thunderResponse is a partial shape of the Thunder API response.
type thunderResponse struct {
	TotalItems int `json:"totalItems"`
	Items      []struct {
		ID        string `json:"id"`
		ReserveID string `json:"reserveId"`
		Title           string `json:"title"`
		IsAvailable     bool   `json:"isAvailable"`
		AvailableCopies int    `json:"availableCopies"`
		OwnedCopies     int    `json:"ownedCopies"`
		HoldsCount      int    `json:"holdsCount"`
		EstimatedWait   int    `json:"estimatedWaitDays"`
		Covers  struct {
			Book struct {
				Href string `json:"href"`
			} `json:"book"`
		} `json:"covers"`
		Formats []struct {
			ID string `json:"id"` // e.g. "ebook-kindle", "ebook-overdrive", "ebook-epub-adobe"
		} `json:"formats"`
	} `json:"items"`
}

// CheckAvailability returns availability for a book at a library.
// It searches by ISBN first, falling back to "title author".
func (s *OverDriveService) CheckAvailability(ctx context.Context, book models.Book, libraryKey string) (models.LibraryResult, error) {
	result := models.LibraryResult{
		LibraryKey: libraryKey,
		Status:     models.StatusNotFound,
	}

	// Build query: prefer ISBN, fall back to title+author
	query := book.BestISBN()
	if query == "" {
		query = fmt.Sprintf("%s %s", book.Title, book.Author)
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

	resp, err := s.fetchThunder(ctx, libraryKey, query)
	if err != nil {
		return result, err
	}

	if resp.TotalItems == 0 || len(resp.Items) == 0 {
		// If ISBN search returned nothing, try title+author
		if book.BestISBN() != "" {
			fallback := fmt.Sprintf("%s %s", book.Title, book.Author)
			resp, err = s.fetchThunder(ctx, libraryKey, fallback)
			if err != nil {
				return result, err
			}
			query = fallback
		}
	}

	if resp.TotalItems > 0 && len(resp.Items) > 0 {
		item := resp.Items[0]
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
			result.OverDriveURL = fmt.Sprintf("https://libbyapp.com/library/%s/everything/page-1/%s", libraryKey, url.PathEscape(mediaID))
		} else {
			// Fallback: Libby title search (always works, lands on search results)
			result.OverDriveURL = fmt.Sprintf("https://libbyapp.com/library/%s/search/query-%s", libraryKey, url.PathEscape(book.Title))
		}

		switch {
		case item.IsAvailable:
			result.Status = models.StatusAvailable
		case item.OwnedCopies > 0:
			result.Status = models.StatusWait
		default:
			result.Status = models.StatusUnavailable
		}

		// Detect Kindle delivery format.
		for _, f := range item.Formats {
			if f.ID == "ebook-kindle" {
				result.HasKindle = true
				break
			}
		}
	}

	if err := s.cache.SetLibrary(libraryKey, query, result); err != nil {
		slog.Warn("cache write error", "err", err)
	}

	return result, nil
}

func (s *OverDriveService) fetchThunder(ctx context.Context, libraryKey, query string) (*thunderResponse, error) {
	endpoint := fmt.Sprintf(thunderBase, url.PathEscape(libraryKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("query", query)
	q.Set("limit", "5")
	q.Set("format", "ebook-overdrive,ebook-epub-adobe,ebook-kindle")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", "shelfprice/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunder API request: %w", err)
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
