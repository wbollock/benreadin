package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// CacheService provides TTL-aware JSON caching backed by SQLite.
type CacheService struct {
	db             *sql.DB
	libraryTTL     int64
	amazonTTL      int64
}

func NewCacheService(db *sql.DB, libraryTTL, amazonTTL int64) *CacheService {
	return &CacheService{db: db, libraryTTL: libraryTTL, amazonTTL: amazonTTL}
}

// GetLibrary retrieves a cached library result. Returns (nil, nil) on miss.
func (c *CacheService) GetLibrary(libraryKey, query string, out interface{}) (bool, error) {
	var jsonStr string
	err := c.db.QueryRow(
		`SELECT result_json FROM library_cache
		 WHERE library_key = ? AND query = ? AND fetched_at > (unixepoch() - ?)`,
		libraryKey, query, c.libraryTTL,
	).Scan(&jsonStr)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cache get library: %w", err)
	}
	return true, json.Unmarshal([]byte(jsonStr), out)
}

// SetLibrary stores a library result in the cache.
func (c *CacheService) SetLibrary(libraryKey, query string, value interface{}) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT INTO library_cache (library_key, query, result_json, fetched_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(library_key, query) DO UPDATE SET result_json=excluded.result_json, fetched_at=excluded.fetched_at`,
		libraryKey, query, string(b), time.Now().Unix(),
	)
	return err
}

// GetAmazon retrieves a cached Amazon result. Returns (nil, nil) on miss.
func (c *CacheService) GetAmazon(isbn string, out interface{}) (bool, error) {
	var jsonStr string
	err := c.db.QueryRow(
		`SELECT result_json FROM amazon_cache
		 WHERE isbn = ? AND fetched_at > (unixepoch() - ?)`,
		isbn, c.amazonTTL,
	).Scan(&jsonStr)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cache get amazon: %w", err)
	}
	return true, json.Unmarshal([]byte(jsonStr), out)
}

// SetAmazon stores an Amazon result in the cache.
func (c *CacheService) SetAmazon(isbn string, value interface{}) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT INTO amazon_cache (isbn, result_json, fetched_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(isbn) DO UPDATE SET result_json=excluded.result_json, fetched_at=excluded.fetched_at`,
		isbn, string(b), time.Now().Unix(),
	)
	return err
}
