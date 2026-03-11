package models

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
}

// BestISBN returns ISBN13 if available, falling back to ISBN10.
func (b *Book) BestISBN() string {
	if b.ISBN13 != "" {
		return b.ISBN13
	}
	return b.ISBN10
}
