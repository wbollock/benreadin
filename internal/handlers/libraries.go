package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wbollock/benreadin/internal/models"
)

const thunderLibraryURL = "https://thunder.api.overdrive.com/v2/libraries/%s"

// LibrariesHandler serves GET /api/libraries?q={query} for autocomplete.
type LibrariesHandler struct {
	db     *sql.DB
	client *http.Client
}

func NewLibrariesHandler(db *sql.DB) *LibrariesHandler {
	return &LibrariesHandler{
		db:     db,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (h *LibrariesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	libs := h.queryLocal(r, q)

	// If nothing found locally and the query looks like a library key
	// (no spaces, reasonable length), try the Thunder API live.
	if len(libs) == 0 && isLibraryKey(q) {
		if lib := h.lookupThunder(r, q); lib != nil {
			libs = []models.Library{*lib}
		}
	}

	if libs == nil {
		libs = []models.Library{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(libs)
}

func (h *LibrariesHandler) queryLocal(r *http.Request, q string) []models.Library {
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
		return nil
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
	return libs
}

// lookupThunder fetches a library record from the OverDrive Thunder API by key,
// caches it in the local DB, and returns the result.
func (h *LibrariesHandler) lookupThunder(r *http.Request, key string) *models.Library {
	url := "https://thunder.api.overdrive.com/v2/libraries/" + key
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil
	}

	resp, err := h.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var body struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Links struct {
			LibraryHome struct {
				Href string `json:"href"`
			} `json:"libraryHome"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Name == "" {
		return nil
	}

	lib := &models.Library{
		Key:     key,
		Name:    body.Name,
		Website: body.Links.LibraryHome.Href,
	}

	// Cache in local DB for future autocomplete hits.
	h.db.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO libraries (key, name, website) VALUES (?, ?, ?)`,
		lib.Key, lib.Name, lib.Website,
	)

	return lib
}

// isLibraryKey returns true if s looks like an OverDrive library key:
// lowercase alphanumeric with optional hyphens, no spaces, 2–40 chars.
func isLibraryKey(s string) bool {
	if len(s) < 2 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}
