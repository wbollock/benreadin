package models

// BookEvent is the full result payload for one book: the enriched book plus
// per-library availability, Amazon pricing, and any Project Gutenberg match.
// It is streamed to clients as an SSE "book" event and stored verbatim in the
// book cache, so the search handler and the prewarm scheduler share it.
type BookEvent struct {
	Book            Book             `json:"book"`
	LibraryResults  []LibraryResult  `json:"library_results"`
	AmazonResult    AmazonResult     `json:"amazon_result"`
	GutenbergResult *GutenbergResult `json:"gutenberg_result,omitempty"`
}
