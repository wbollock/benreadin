package db

import (
	"path/filepath"
	"testing"
)

// The DSN must use modernc's _pragma=name(value) form: mattn-style params are
// silently ignored, which once left production on journal_mode=delete with no
// busy timeout.
func TestOpenAppliesPragmas(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"), 60, 60, 60, 60, 60)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestPurgeStaleKeepsHardcoverShelves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purge.db")
	d, err := Open(path, 3600, 3600, 3600, 300, 86400)
	if err != nil {
		t.Fatal(err)
	}
	day := int64(24 * 3600)
	for _, row := range []struct {
		key string
		age int64
	}{{"97106512|to-read", day}, {"@fresh|to-read", day}, {"@old|to-read", 8 * day}} {
		if _, err := d.Exec(`INSERT INTO shelf_cache (cache_key, events_json, fetched_at) VALUES (?, '[]', unixepoch() - ?)`, row.key, row.age); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	d, err = Open(path, 3600, 3600, 3600, 300, 86400) // re-open runs purgeStale
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var keys []string
	rows, err := d.Query(`SELECT cache_key FROM shelf_cache ORDER BY cache_key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != "@fresh|to-read" {
		t.Fatalf("remaining keys = %v, want only @fresh|to-read", keys)
	}
}
