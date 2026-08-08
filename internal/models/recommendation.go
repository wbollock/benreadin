package models

// Recommendation is a book from the user's taste profile, enriched exactly
// like a search result — same book fields, all per-library statuses, Amazon
// pricing, Gutenberg match — plus why it was suggested. The frontend renders
// it with the same book-card component as ordinary search results.
type Recommendation struct {
	Book            Book             `json:"book"`
	LibraryResults  []LibraryResult  `json:"library_results"`
	AmazonResult    AmazonResult     `json:"amazon_result"`
	GutenbergResult *GutenbergResult `json:"gutenberg_result,omitempty"`

	// Which engine produced this rec: "series" | "author" | "subject".
	Source string `json:"source"`
	// Human-readable provenance, exactly one set per rec.
	BecauseSeries  string `json:"because_series,omitempty"`
	BecauseAuthor  string `json:"because_author,omitempty"`
	BecauseSubject string `json:"because_subject,omitempty"` // phase 2
}
