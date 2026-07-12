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

// Open opens (or creates) the SQLite database at path, runs migrations, and
// purges stale cache rows older than the given TTLs.
func Open(path string, libraryTTL, amazonTTL, bookTTL, shelfTTL int64) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	// modernc.org/sqlite only understands the _pragma=name(value) DSN form —
	// mattn-style params (_journal_mode=WAL, _busy_timeout=…) are silently
	// ignored, leaving the DB in journal_mode=delete with no busy timeout.
	db, err := sql.Open("sqlite", path+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=busy_timeout(5000)"+
		"&_pragma=foreign_keys(on)"+
		"&_pragma=synchronous(normal)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL allows readers concurrent with the single writer; busy_timeout
	// serializes writer collisions instead of surfacing SQLITE_BUSY.
	db.SetMaxOpenConns(8)

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if err := purgeStale(db, libraryTTL, amazonTTL, bookTTL, shelfTTL); err != nil {
		return nil, fmt.Errorf("purge stale: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

func purgeStale(db *sql.DB, libraryTTL, amazonTTL, bookTTL, shelfTTL int64) error {
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
		shelfTTL,
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
