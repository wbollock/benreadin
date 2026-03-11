package services

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/wbollock/shelfprice/internal/models"
)

const (
	gutenbergCatalogURL = "https://www.gutenberg.org/cache/epub/feeds/pg_catalog.csv"
	// Re-download catalog if older than this many seconds (7 days).
	gutenbergRefreshTTL = 7 * 24 * 60 * 60
)

// GutenbergService matches books against the Project Gutenberg catalog.
type GutenbergService struct {
	db     *sql.DB
	client *http.Client
}

func NewGutenbergService(db *sql.DB) *GutenbergService {
	return &GutenbergService{
		db:     db,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// LoadCatalog downloads and indexes the Gutenberg catalog if it is missing or stale.
func (g *GutenbergService) LoadCatalog(ctx context.Context) error {
	// Check whether the catalog is current.
	var count int
	var maxID int
	_ = g.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(id),0) FROM gutenberg_books`).Scan(&count, &maxID)

	// Check catalog age via a sentinel row (id = 0 stores the fetch timestamp).
	var fetchedAt int64
	_ = g.db.QueryRowContext(ctx, `SELECT id FROM gutenberg_books WHERE id = 0`).Scan(&fetchedAt)
	age := time.Now().Unix() - fetchedAt
	if count > 1 && age < gutenbergRefreshTTL {
		slog.Info("gutenberg catalog up to date", "entries", count-1)
		return nil
	}

	slog.Info("loading gutenberg catalog...")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gutenbergCatalogURL, nil)
	if err != nil {
		return fmt.Errorf("gutenberg catalog request: %w", err)
	}
	req.Header.Set("User-Agent", "ShelfPrice/1.0 (catalog import)")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gutenberg catalog fetch: %w", err)
	}
	defer resp.Body.Close()

	r := csv.NewReader(resp.Body)
	r.LazyQuotes = true

	// Read header to find column indices.
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("gutenberg csv header: %w", err)
	}
	colIdx := map[string]int{}
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}
	idCol, hasID := colIdx["Text#"]
	typeCol, hasType := colIdx["Type"]
	titleCol, hasTitle := colIdx["Title"]
	langCol, hasLang := colIdx["Language"]
	authCol, hasAuth := colIdx["Authors"]
	if !hasID || !hasType || !hasTitle || !hasLang || !hasAuth {
		return fmt.Errorf("gutenberg csv missing expected columns: %v", header)
	}

	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gutenberg tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM gutenberg_books`); err != nil {
		return fmt.Errorf("gutenberg clear: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO gutenberg_books (id, title, author, epub_url) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("gutenberg prepare: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed rows
		}
		if len(row) <= max(idCol, typeCol, titleCol, langCol, authCol) {
			continue
		}

		// Only English prose texts.
		if strings.TrimSpace(row[typeCol]) != "Text" {
			continue
		}
		if !strings.Contains(strings.ToLower(row[langCol]), "en") {
			continue
		}

		id, err := strconv.Atoi(strings.TrimSpace(row[idCol]))
		if err != nil || id <= 0 {
			continue
		}

		title := strings.TrimSpace(row[titleCol])
		author := normalizeGutenbergAuthor(strings.TrimSpace(row[authCol]))
		epubURL := fmt.Sprintf("https://www.gutenberg.org/ebooks/%d.epub.images", id)

		if _, err := stmt.ExecContext(ctx, id, title, author, epubURL); err != nil {
			continue
		}
		inserted++
	}

	// Store a sentinel row with id=0 to track when we last fetched.
	_, _ = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO gutenberg_books (id, title, author, epub_url) VALUES (0, '', '', ?)`,
		strconv.FormatInt(time.Now().Unix(), 10),
	)

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gutenberg commit: %w", err)
	}
	slog.Info("gutenberg catalog loaded", "entries", inserted)
	return nil
}

// Lookup finds a Gutenberg entry matching title + author. Returns nil on no match.
func (g *GutenbergService) Lookup(title, author string) *models.GutenbergResult {
	normTitle := normalizeTitle(title)
	normAuthor := normalizeAuthor(author)

	// Try exact normalized title match first.
	rows, err := g.db.Query(
		`SELECT id, epub_url FROM gutenberg_books
		 WHERE id > 0 AND LOWER(title) = ? LIMIT 5`,
		strings.ToLower(normTitle),
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var epubURL string
			if err := rows.Scan(&id, &epubURL); err != nil {
				continue
			}
			// Accept any author on exact title match — Gutenberg sometimes lists
			// different editions with slightly different author strings.
			return &models.GutenbergResult{ID: id, EPUBURL: epubURL}
		}
	}

	// Fall back to LIKE title match + author check.
	rows2, err := g.db.Query(
		`SELECT id, author, epub_url FROM gutenberg_books
		 WHERE id > 0 AND LOWER(title) LIKE ? LIMIT 10`,
		"%"+strings.ToLower(normTitle)+"%",
	)
	if err != nil {
		return nil
	}
	defer rows2.Close()
	for rows2.Next() {
		var id int
		var gAuthor, epubURL string
		if err := rows2.Scan(&id, &gAuthor, &epubURL); err != nil {
			continue
		}
		if authorsMatch(normAuthor, gAuthor) {
			return &models.GutenbergResult{ID: id, EPUBURL: epubURL}
		}
	}
	return nil
}

// normalizeTitle strips articles and punctuation for comparison.
func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	// Remove leading articles.
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			s = s[len(prefix):]
			break
		}
	}
	// Strip punctuation and collapse spaces.
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// normalizeAuthor converts "First Last" → "last first" for comparison.
func normalizeAuthor(s string) string {
	parts := strings.Fields(strings.ToLower(s))
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	// Move last name to front: "first last" → "last first"
	return parts[len(parts)-1] + " " + strings.Join(parts[:len(parts)-1], " ")
}

// normalizeGutenbergAuthor converts Gutenberg's "Last, First" → "last first".
func normalizeGutenbergAuthor(s string) string {
	// Gutenberg may list multiple authors separated by ";"; take the first.
	s = strings.SplitN(s, ";", 2)[0]
	// Remove birth/death years in parentheses: "Austen, Jane (1775-1817)" → "Austen, Jane"
	if i := strings.Index(s, "("); i != -1 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// "Last, First" → "last first"
	if idx := strings.Index(s, ","); idx != -1 {
		last := strings.TrimSpace(s[:idx])
		first := strings.TrimSpace(s[idx+1:])
		return strings.ToLower(last + " " + first)
	}
	return strings.ToLower(s)
}

// authorsMatch checks whether the Goodreads author and the Gutenberg author
// refer to the same person using a loose substring check.
func authorsMatch(goodreads, gutenberg string) bool {
	if goodreads == "" || gutenberg == "" {
		return true // can't tell — accept
	}
	// Extract just the last name from each.
	grLast := strings.Fields(goodreads)
	gbLast := strings.Fields(gutenberg)
	if len(grLast) == 0 || len(gbLast) == 0 {
		return false
	}
	// Compare last names (first token after normalization for both).
	return grLast[0] == gbLast[0]
}

func max(a, b int, rest ...int) int {
	m := a
	if b > m {
		m = b
	}
	for _, v := range rest {
		if v > m {
			m = v
		}
	}
	return m
}
