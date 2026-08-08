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
