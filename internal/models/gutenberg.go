package models

// GutenbergResult holds Project Gutenberg data for a free ebook match.
type GutenbergResult struct {
	ID      int    `json:"id"`
	EPUBURL string `json:"epub_url"`
}
