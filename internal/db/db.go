package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ShelfCacheTTL is the default TTL for full shelf search result caches (5 minutes).
const ShelfCacheTTL int64 = 300

// Open opens (or creates) the SQLite database at path, runs migrations, and
// purges stale cache rows older than the given TTLs.
func Open(path string, libraryTTL, amazonTTL, bookTTL int64) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if err := purgeStale(db, libraryTTL, amazonTTL, bookTTL); err != nil {
		return nil, fmt.Errorf("purge stale: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

func purgeStale(db *sql.DB, libraryTTL, amazonTTL, bookTTL int64) error {
	_, err := db.Exec(
		`DELETE FROM library_cache WHERE fetched_at <= (unixepoch() - ?)`,
		libraryTTL,
	)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`DELETE FROM amazon_cache WHERE fetched_at <= (unixepoch() - ?)`,
		amazonTTL,
	)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`DELETE FROM shelf_cache WHERE fetched_at <= (unixepoch() - ?)`,
		ShelfCacheTTL,
	)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`DELETE FROM book_cache WHERE fetched_at <= (unixepoch() - ?)`,
		bookTTL,
	)
	return err
}
