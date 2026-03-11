package models

// Recommendation is a book suggested based on the user's shelf that is
// currently available to borrow from at least one of their libraries.
type Recommendation struct {
	Title          string          `json:"title"`
	Author         string          `json:"author"`
	CoverURL       string          `json:"cover_url"`
	Description    string          `json:"description"`
	ISBN13         string          `json:"isbn13"`
	LibraryResults []LibraryResult `json:"library_results"`
	// The shelf book that triggered this recommendation
	BecauseOfTitle string `json:"because_of_title"`
}
