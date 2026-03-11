package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/wbollock/shelfprice/internal/services"
)

// RecommendationsHandler handles GET /api/recommendations.
type RecommendationsHandler struct {
	goodreads       *services.GoodreadsService
	recommendations *services.RecommendationService
}

func NewRecommendationsHandler(gr *services.GoodreadsService, recs *services.RecommendationService) *RecommendationsHandler {
	return &RecommendationsHandler{goodreads: gr, recommendations: recs}
}

func (h *RecommendationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawURL := r.URL.Query().Get("url")
	libraries := r.URL.Query()["libraries"]

	if rawURL == "" {
		http.Error(w, `{"error":"url parameter required"}`, http.StatusBadRequest)
		return
	}

	parsed, err := services.ParseShelfURL(rawURL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	if len(libraries) == 0 {
		libraries = parsed.Libraries
	}
	if len(libraries) == 0 {
		http.Error(w, `{"error":"at least one library key required"}`, http.StatusBadRequest)
		return
	}

	// Fetch the shelf so we know what books to base recommendations on.
	books, err := h.goodreads.FetchShelf(ctx, parsed.UserID, parsed.Shelf)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	recs := h.recommendations.FindRecommendations(ctx, books, libraries)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}
