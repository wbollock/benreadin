package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/wbollock/benreadin/internal/metrics"
	"github.com/wbollock/benreadin/internal/models"
	"github.com/wbollock/benreadin/internal/services"
)

// RecommendationsHandler handles GET /api/recommendations as an SSE stream.
type RecommendationsHandler struct {
	recommendations *services.RecommendationService
}

func NewRecommendationsHandler(recs *services.RecommendationService) *RecommendationsHandler {
	return &RecommendationsHandler{recommendations: recs}
}

func (h *RecommendationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawURL := r.URL.Query().Get("url")
	libraries := r.URL.Query()["libraries"]
	refresh := r.URL.Query().Get("refresh") == "true"

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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var sseMu sync.Mutex
	sendEvent := func(eventType string, data interface{}) {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		sseMu.Lock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(b))
		flusher.Flush()
		sseMu.Unlock()
	}

	// Same rationale as /api/search: push past Firefox's ~1KB dispatch
	// threshold immediately so the client doesn't sit frozen on connect.
	sseMu.Lock()
	fmt.Fprintf(w, ": %s\n\n", ssePadding)
	flusher.Flush()
	sseMu.Unlock()

	count := 0
	err = h.recommendations.Stream(ctx, parsed.UserID, libraries, refresh,
		func(rec models.Recommendation) {
			count++
			sendEvent("rec", rec)
		},
		func(p services.RecProgress) {
			sendEvent("rec_progress", p)
		},
	)
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("recommendations").Inc()
		sendEvent("error", map[string]string{"message": err.Error()})
		return
	}

	sendEvent("recs_done", map[string]int{"count": count})
}
