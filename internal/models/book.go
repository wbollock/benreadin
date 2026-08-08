package models

import (
	"regexp"
	"strings"
)

// Book represents a single book from a Goodreads shelf.
type Book struct {
	GoodreadsID   string  `json:"goodreads_id"`
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
