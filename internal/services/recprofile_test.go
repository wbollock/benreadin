package services

import (
	"reflect"
	"sort"
	"testing"

	"github.com/wbollock/benreadin/internal/models"
)

func TestParseSeriesAnnotation(t *testing.T) {
	tests := []struct {
		title               string
		wantName            string
		wantFirst, wantLast int
		wantOK              bool
	}{
		{"The Gunslinger (The Dark Tower, #1)", "The Dark Tower", 1, 1, true},
		{"A Parade of Horribles (Dungeon Crawler Carl, #8)", "Dungeon Crawler Carl", 8, 8, true},
		{"The Fellowship of the Ring (The Lord of the Rings, #1-3)", "The Lord of the Rings", 1, 3, true},
		// Double annotation: resolve to the first (main) series.
		{"Night Watch (Discworld, #29; City Watch, #6)", "Discworld", 29, 29, true},
		{"Educated", "", 0, 0, false},
		{"Frankenstein (Illustrated Edition)", "", 0, 0, false},
	}
	for _, tt := range tests {
		name, first, last, ok := parseSeriesAnnotation(tt.title)
		if ok != tt.wantOK || name != tt.wantName || first != tt.wantFirst || last != tt.wantLast {
			t.Errorf("parseSeriesAnnotation(%q) = (%q, %d, %d, %v), want (%q, %d, %d, %v)",
				tt.title, name, first, last, ok, tt.wantName, tt.wantFirst, tt.wantLast, tt.wantOK)
		}
	}
}

func TestRecTitleKey(t *testing.T) {
	tests := []struct{ a, b string }{
		{"In the Kingdom of Ice", "In the Kingdom of Ice: The Grand and Terrible Polar Voyage of the USS Jeannette"},
		{"The Gunslinger (The Dark Tower, #1)", "The Gunslinger"},
		{"Educated", "Educated: A Memoir"},
	}
	for _, tt := range tests {
		ka, kb := recTitleKey(tt.a), recTitleKey(tt.b)
		if ka != kb || ka == "" {
			t.Errorf("recTitleKey(%q)=%q, recTitleKey(%q)=%q; want equal and non-empty", tt.a, ka, tt.b, kb)
		}
	}
}

func TestAuthorMatchKeyAndQueryName(t *testing.T) {
	tests := []struct {
		name      string
		wantMatch string
		wantQuery string
	}{
		{"Stephen  King", "stephen king", "Stephen King"},
		{"Kurt Vonnegut Jr.", "kurt vonnegut", "Kurt Vonnegut"},
		{"George R. R. Martin", "george martin", "George R. R. Martin"},
		{"Terry Pratchett", "terry pratchett", "Terry Pratchett"},
	}
	for _, tt := range tests {
		if got := authorMatchKey(tt.name); got != tt.wantMatch {
			t.Errorf("authorMatchKey(%q) = %q, want %q", tt.name, got, tt.wantMatch)
		}
		if got := authorQueryName(tt.name); got != tt.wantQuery {
			t.Errorf("authorQueryName(%q) = %q, want %q", tt.name, got, tt.wantQuery)
		}
	}

	// The bug this guards against: two different authors sharing a common
	// surname must NOT match each other.
	if authorMatchKey("Madeline Miller") == authorMatchKey("Linsey Miller") {
		t.Error("authorMatchKey must distinguish different authors with the same surname")
	}
}

func TestParseReadingOrder(t *testing.T) {
	tests := []struct {
		s      string
		want   float64
		wantOK bool
	}{
		{"20", 20, true},
		{"3", 3, true},
		{"3.5", 0, false}, // novella side-story, not a mainline entry
		{"", 0, false},
		{"not-a-number", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseReadingOrder(tt.s)
		if ok != tt.wantOK || (ok && got != tt.want) {
			t.Errorf("parseReadingOrder(%q) = (%v, %v), want (%v, %v)", tt.s, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestBuildRecProfileAuthorWeighting(t *testing.T) {
	read := []models.Book{
		{Title: "Book A", Author: "Isaac Asimov", UserRating: 5},
		{Title: "Book B", Author: "Isaac Asimov", UserRating: 0}, // unrated finished — still positive
		{Title: "Book C", Author: "Solo Author", UserRating: 0},  // single unrated finished book: weight 1, below threshold
		{Title: "Book D", Author: "Bad Author", UserRating: 1},
		{Title: "Book E", Author: "Bad Author", UserRating: 2},
	}
	profile := buildRecProfile(read, nil)

	byName := make(map[string]RecAuthor)
	for _, a := range profile.Authors {
		byName[a.Name] = a
	}

	asimov, ok := byName["Isaac Asimov"]
	if !ok {
		t.Fatalf("expected Isaac Asimov in profile, got %+v", profile.Authors)
	}
	if asimov.Weight != 3 { // 2 (5-star) + 1 (unrated finished)
		t.Errorf("Isaac Asimov weight = %v, want 3", asimov.Weight)
	}
	if asimov.Finished != 2 {
		t.Errorf("Isaac Asimov finished = %d, want 2", asimov.Finished)
	}

	if _, ok := byName["Solo Author"]; ok {
		t.Errorf("Solo Author (weight 1) should be below the %v threshold and excluded", minAuthorWeight)
	}
	if _, ok := byName["Bad Author"]; ok {
		t.Errorf("Bad Author (two low ratings, negative weight) should be excluded")
	}
}

func TestBuildRecProfileSeriesProgress(t *testing.T) {
	read := []models.Book{
		{Title: "Carl's Doomsday Scenario (Dungeon Crawler Carl, #2)", UserRating: 5},
		{Title: "Dungeon Crawler Carl (Dungeon Crawler Carl, #1)", UserRating: 0},
	}
	want := []models.Book{
		{Title: "The Butcher's Masquerade (Dungeon Crawler Carl, #5)"},
	}
	profile := buildRecProfile(read, want)

	if len(profile.Series) != 1 {
		t.Fatalf("expected 1 series, got %d: %+v", len(profile.Series), profile.Series)
	}
	sp := profile.Series[0]
	if sp.MaxRead != 2 {
		t.Errorf("MaxRead = %d, want 2", sp.MaxRead)
	}
	next, ok := sp.NextUnread()
	if !ok || next != 3 {
		t.Errorf("NextUnread() = (%d, %v), want (3, true)", next, ok)
	}

	// Next entry already shelved on to-read → no recommendation needed.
	sp2 := RecSeries{MaxRead: 4, Shelved: []int{1, 2, 3, 4, 5}}
	if _, ok := sp2.NextUnread(); ok {
		t.Errorf("NextUnread() should report false when #5 is already shelved")
	}
}

func TestBuildRecProfileSeriesSuppressedByLowRating(t *testing.T) {
	read := []models.Book{
		{Title: "Some Book (Bad Series, #1)", UserRating: 1},
	}
	profile := buildRecProfile(read, nil)
	if len(profile.Series) != 0 {
		t.Errorf("a series where the only finished entry was rated 1-2 stars should not be recommended further: got %+v", profile.Series)
	}
}

func TestBuildRecProfileExclusionSet(t *testing.T) {
	read := []models.Book{{Title: "In the Kingdom of Ice: The Grand and Terrible Polar Voyage of the USS Jeannette"}}
	want := []models.Book{{Title: "The Gunslinger (The Dark Tower, #1)"}}
	profile := buildRecProfile(read, want)

	sort.Strings(profile.Excluded)
	got := profile.Excluded
	wantKeys := []string{recTitleKey("In the Kingdom of Ice"), recTitleKey("The Gunslinger")}
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("Excluded = %v, want %v", got, wantKeys)
	}
}

func TestRecStateDedupeAndCap(t *testing.T) {
	rs := newRecState([]string{"shelved-book"}, 2)

	if rs.tryEmit("shelved-book") {
		t.Error("tryEmit should reject a title already on the exclusion list")
	}
	if !rs.tryEmit("a") {
		t.Error("tryEmit should accept a fresh title")
	}
	if rs.tryEmit("a") {
		t.Error("tryEmit should reject a title already emitted")
	}
	if !rs.tryEmit("b") {
		t.Error("tryEmit should accept a second fresh title (cap is 2)")
	}
	if rs.tryEmit("c") {
		t.Error("tryEmit should reject once the cap is reached")
	}
	if !rs.full() {
		t.Error("full() should report true once the cap is reached")
	}
}

func TestAnyAvailableKindle(t *testing.T) {
	yes := []models.LibraryResult{
		{Status: models.StatusWait},
		{Status: models.StatusAvailable, HasKindle: true},
	}
	no := []models.LibraryResult{
		{Status: models.StatusAvailable, HasKindle: false},
		{Status: models.StatusWait, HasKindle: true},
	}
	if !anyAvailableKindle(yes) {
		t.Error("expected anyAvailableKindle(yes) = true")
	}
	if anyAvailableKindle(no) {
		t.Error("expected anyAvailableKindle(no) = false")
	}
}
