package services

import (
	"path/filepath"
	"testing"

	"github.com/wbollock/benreadin/internal/db"
)

func newTestCache(t *testing.T) *CacheService {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"), 3600, 3600, 3600, 300, 86400)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return NewCacheService(database, 3600, 3600, 3600, 300, 86400)
}

func TestGetBooksBatch(t *testing.T) {
	cache := newTestCache(t)
	libsKey := "lapl,nypl"

	type payload struct {
		Title string `json:"title"`
	}
	if err := cache.SetBook("101", libsKey, payload{Title: "Dune"}); err != nil {
		t.Fatalf("SetBook: %v", err)
	}
	if err := cache.SetBook("102", libsKey, payload{Title: "Hyperion"}); err != nil {
		t.Fatalf("SetBook: %v", err)
	}
	// Same book under a different library set must not leak into this query.
	if err := cache.SetBook("103", "other", payload{Title: "Blindsight"}); err != nil {
		t.Fatalf("SetBook: %v", err)
	}

	got, err := cache.GetBooks([]string{"101", "102", "103", "104"}, libsKey)
	if err != nil {
		t.Fatalf("GetBooks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d cached events, want 2 (keys: %v)", len(got), got)
	}
	for _, id := range []string{"101", "102"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing cached event for id %s", id)
		}
	}
	if _, ok := got["103"]; ok {
		t.Errorf("id 103 cached under a different library set must miss")
	}

	// Empty input should not build a malformed IN () query.
	if got, err := cache.GetBooks(nil, libsKey); err != nil || len(got) != 0 {
		t.Errorf("GetBooks(nil) = (%v, %v), want empty map, nil error", got, err)
	}
}

func TestShelfCacheRoundTrip(t *testing.T) {
	cache := newTestCache(t)

	type book struct {
		Title string `json:"title"`
	}
	var out []book
	if hit, err := cache.GetShelf("42|to-read", &out); err != nil || hit {
		t.Fatalf("GetShelf on empty cache = (%v, %v), want miss", hit, err)
	}

	want := []book{{Title: "Dune"}, {Title: "Hyperion"}}
	if err := cache.SetShelf("42|to-read", want); err != nil {
		t.Fatalf("SetShelf: %v", err)
	}
	hit, err := cache.GetShelf("42|to-read", &out)
	if err != nil || !hit {
		t.Fatalf("GetShelf = (%v, %v), want hit", hit, err)
	}
	if len(out) != 2 || out[0].Title != "Dune" {
		t.Errorf("GetShelf returned %+v, want %+v", out, want)
	}
}
