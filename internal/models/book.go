package models

import (
	"regexp"
	"strings"
)

// Book represents a single book from a Goodreads or Hardcover shelf.
type Book struct {
	// GoodreadsID is the app-wide book key (book cache, DOM ids). For
	// Hardcover books it holds "hc-<hardcover book id>" — see Source.
	GoodreadsID   string  `json:"goodreads_id"`
	Source        string  `json:"source,omitempty"`   // "hardcover" | "" (Goodreads)
	BookURL       string  `json:"book_url,omitempty"` // source book page; empty → derive Goodreads URL
	// SeriesName/SeriesPosition come from structured source data (Hardcover).
	// Goodreads books leave them empty; their series lives in the title's
	// "(Series, #N)" annotation instead.
	SeriesName     string `json:"series_name,omitempty"`
	SeriesPosition int    `json:"series_position,omitempty"`
	Title         string  `json:"title"`
	Author        string  `json:"author"`
	ISBN10        string  `json:"isbn10"`
	ISBN13        string  `json:"isbn13"`
	CoverURL      string  `json:"cover_url"`
	Description   string  `json:"description"`
	AverageRating float64 `json:"average_rating,omitempty"`
	UserRating    int     `json:"user_rating,omitempty"`
	PageCount     int     `json:"page_count,omitempty"`

	// RatingsCount and RatingSource accompany AverageRating when it was
	// backfilled from Open Library (recommendations, which have no Goodreads
	// rating of their own) rather than read from the Goodreads shelf RSS.
	RatingsCount int    `json:"ratings_count,omitempty"`
	RatingSource string `json:"rating_source,omitempty"` // "openlibrary" | "" (Goodreads)

	// Genre is a coarse fiction/nonfiction classification backfilled from
	// OverDrive catalog subject tags once availability is checked (see
	// LibraryResult.Genre / GenreFromResults). Empty when unclassified.
	Genre string `json:"genre,omitempty"` // "fiction" | "nonfiction" | ""

	// DateAddedUnix is when the book was added to this Goodreads shelf (from
	// the RSS <user_date_added> field), as a Unix timestamp so the JSON
	// omitempty check works cleanly (unlike the zero value of time.Time).
	// Zero when Goodreads didn't supply it.
	DateAddedUnix int64 `json:"date_added_unix,omitempty"`
}

// Goodreads appends series info as a trailing parenthetical containing "#",
// e.g. "The Gunslinger (The Dark Tower, #1)".
var reSeriesSuffix = regexp.MustCompile(`\s*\([^)]*#[^)]*\)$`)

// SearchTitle returns Title without any trailing Goodreads series annotation.
// The annotation poisons text searches against external catalogs — Thunder
// ranks unrelated series tie-ins above the book itself.
func (b *Book) SearchTitle() string {
	return strings.TrimSpace(reSeriesSuffix.ReplaceAllString(b.Title, ""))
}

// BestISBN returns ISBN13 if available, falling back to ISBN10.
func (b *Book) BestISBN() string {
	if b.ISBN13 != "" {
		return b.ISBN13
	}
	return b.ISBN10
}
