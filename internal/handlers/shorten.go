package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const tokenLength = 7

// ShortenHandler handles POST /api/shorten and GET /s/{token}.
type ShortenHandler struct {
	db *sql.DB
}

func NewShortenHandler(db *sql.DB) *ShortenHandler {
	return &ShortenHandler{db: db}
}

func (h *ShortenHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		URL       string   `json:"url"`
		Libraries []string `json:"libraries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	libs := strings.Join(body.Libraries, ",")

	// Reuse an existing token for the same url+libraries combo.
	var existing string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT token FROM shortlinks WHERE url = ? AND libraries = ? LIMIT 1`,
		body.URL, libs,
	).Scan(&existing)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": existing, "link": "/s/" + existing})
		return
	}

	token, err := newToken()
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO shortlinks (token, url, libraries) VALUES (?, ?, ?)`,
		token, body.URL, libs,
	)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token, "link": "/s/" + token})
}

func (h *ShortenHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	var url, libraries string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT url, libraries FROM shortlinks WHERE token = ? LIMIT 1`,
		token,
	).Scan(&url, &libraries)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	q := "url=" + encodeURIComponent(url)
	for _, lib := range strings.Split(libraries, ",") {
		if lib != "" {
			q += "&libraries=" + encodeURIComponent(lib)
		}
	}
	http.Redirect(w, r, "/results.html?"+q, http.StatusFound)
}

func newToken() (string, error) {
	b := make([]byte, tokenLength)
	alphabetLen := big.NewInt(int64(len(tokenAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", err
		}
		b[i] = tokenAlphabet[n.Int64()]
	}
	return string(b), nil
}

func encodeURIComponent(s string) string {
	// net/url.QueryEscape encodes spaces as + — we want %20 for path safety
	var buf strings.Builder
	for _, b := range []byte(s) {
		if isUnreserved(b) {
			buf.WriteByte(b)
		} else {
			buf.WriteString("%" + hexByte(b))
		}
	}
	return buf.String()
}

func isUnreserved(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.' || b == '~'
}

func hexByte(b byte) string {
	const hx = "0123456789ABCDEF"
	return string([]byte{hx[b>>4], hx[b&0xf]})
}
