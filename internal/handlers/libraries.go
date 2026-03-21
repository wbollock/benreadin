package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wbollock/benreadin/internal/models"
)

// LibrariesHandler serves GET /api/libraries?q={query} for autocomplete.
type LibrariesHandler struct {
	db *sql.DB
}

func NewLibrariesHandler(db *sql.DB) *LibrariesHandler {
	return &LibrariesHandler{db: db}
}

func (h *LibrariesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	var (
		rows *sql.Rows
		err  error
	)

	if q == "" {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT key, name, COALESCE(website,'') FROM libraries ORDER BY name LIMIT 100`)
	} else {
		like := "%" + q + "%"
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT key, name, COALESCE(website,'') FROM libraries
			 WHERE name LIKE ? OR key LIKE ?
			 ORDER BY name LIMIT 20`,
			like, like,
		)
	}

	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var libs []models.Library
	for rows.Next() {
		var l models.Library
		if err := rows.Scan(&l.Key, &l.Name, &l.Website); err != nil {
			continue
		}
		libs = append(libs, l)
	}

	if libs == nil {
		libs = []models.Library{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(libs)
}
