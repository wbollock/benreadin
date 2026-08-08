package services

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wbollock/benreadin/internal/models"
)

// RecProfile is a compact summary of a user's Goodreads taste, built from the
// read and to-read shelves and cached in the rec_profile table. Finishing a
// book counts as a positive signal even when unrated — the profile's owner
// rarely rates, but generally liked what they finished.
type RecProfile struct {
	// Authors the user has finished, sorted by descending weight. Authors
	// whose weight fell below the threshold (e.g. dragged down by 1-2★
	// ratings) are omitted.
	Authors []RecAuthor `json:"authors"`
	// Series with at least one finished book.
	Series []RecSeries `json:"series"`
	// Normalized title keys (see recTitleKey) of every book on either shelf.
	Excluded []string `json:"excluded"`
}

// RecAuthor is one author in the taste profile.
type RecAuthor struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Finished int     `json:"finished"` // count of finished books, for "because" lines
}

// RecSeries tracks reading progress through one series.
type RecSeries struct {
	Name    string `json:"name"`     // display name as annotated by Goodreads
	MaxRead int    `json:"max_read"` // highest finished entry number
	Shelved []int  `json:"shelved"`  // every entry number on either shelf
}

// NextUnread returns the next series entry to recommend, or false when it is
// already shelved.
func (s RecSeries) NextUnread() (int, bool) {
	next := s.MaxRead + 1
	for _, n := range s.Shelved {
		if n == next {
			return 0, false
		}
	}
	return next, true
}

// reSeriesAnnotation parses Goodreads' trailing series annotation:
// "(Series Name, #3)" or "(Series Name, #1-3)". Sub-series annotations like
// "(Discworld, #15; City Watch, #2)" resolve to the first (main) series.
var reSeriesAnnotation = regexp.MustCompile(`\(([^()#]+?)[,;]?\s*#(\d+)(?:-(\d+))?[^()]*\)\s*$`)

// parseSeriesAnnotation extracts (series name, first entry, last entry) from a
// Goodreads title. first == last except for omnibus ranges like "#1-3".
func parseSeriesAnnotation(title string) (name string, first, last int, ok bool) {
	m := reSeriesAnnotation.FindStringSubmatch(title)
	if m == nil {
		return "", 0, 0, false
	}
	name = strings.TrimSpace(m[1])
	first, _ = strconv.Atoi(m[2])
	last = first
	if m[3] != "" {
		if n, err := strconv.Atoi(m[3]); err == nil && n > first {
			last = n
		}
	}
	if name == "" {
		return "", 0, 0, false
	}
	return name, first, last, true
}

// recTitleKey normalizes a title for exclusion and dedup comparisons: series
// annotation off, subtitle off, articles and punctuation normalized. Thunder
// drops subtitles ("In the Kingdom of Ice" vs the shelf's "In the Kingdom of
// Ice: The Grand and Terrible…"), so both sides must reduce to the same key.
func recTitleKey(title string) string {
	b := models.Book{Title: title}
	return normalizeTitle(stripSubtitle(b.SearchTitle()))
}

// normalizeAuthorName collapses runs of whitespace — Goodreads emits names
// like "Stephen  King" with doubled spaces.
func normalizeAuthorName(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// authorMatchKey returns "firstname lastname" (lowercased, periods and
// generational suffixes stripped, middle names/initials ignored) so a
// Thunder result's creator can be checked against the profile author.
// Surname alone is too loose — Thunder returned "Being Ace" by Linsey Miller
// as a match for profile author Madeline Miller on a surname-only compare.
func authorMatchKey(name string) string {
	fields := strings.Fields(strings.ToLower(name))
	for len(fields) > 1 {
		switch strings.Trim(fields[len(fields)-1], ".") {
		case "jr", "sr", "ii", "iii", "iv":
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return ""
	}
	first := strings.Trim(fields[0], ".")
	last := strings.Trim(fields[len(fields)-1], ".")
	if first == last {
		return first
	}
	return first + " " + last
}

// authorQueryName strips generational suffixes for the Thunder search query —
// "Kurt Vonnegut Jr." finds nothing, "Kurt Vonnegut" finds everything.
func authorQueryName(name string) string {
	fields := strings.Fields(name)
	for len(fields) > 1 {
		switch strings.Trim(strings.ToLower(fields[len(fields)-1]), ".") {
		case "jr", "sr", "ii", "iii", "iv":
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	return strings.Join(fields, " ")
}

// minAuthorWeight is the profile-weight threshold for author expansion:
// two finished books, or one rated 4-5★.
const minAuthorWeight = 2.0

// buildRecProfile derives a taste profile from the read and to-read shelves.
// Weighting: finished 4-5★ = 2, finished unrated = 1, finished 3★ = 0.5,
// finished 1-2★ = −2 (and suppresses the book's series entirely).
func buildRecProfile(read, want []models.Book) *RecProfile {
	weights := make(map[string]float64)
	finished := make(map[string]int)
	display := make(map[string]string)

	series := make(map[string]*RecSeries)
	seriesSuppressed := make(map[string]bool)

	var excluded []string
	seenKeys := make(map[string]bool)
	exclude := func(title string) {
		if k := recTitleKey(title); k != "" && !seenKeys[k] {
			seenKeys[k] = true
			excluded = append(excluded, k)
		}
	}
	trackSeries := func(title string, isRead bool, lowRated bool) {
		name, first, last, ok := parseSeriesAnnotation(title)
		if !ok {
			return
		}
		key := strings.ToLower(name)
		sp := series[key]
		if sp == nil {
			sp = &RecSeries{Name: name}
			series[key] = sp
		}
		for n := first; n <= last; n++ {
			sp.Shelved = append(sp.Shelved, n)
		}
		if isRead && last > sp.MaxRead {
			sp.MaxRead = last
		}
		if lowRated {
			seriesSuppressed[key] = true
		}
	}

	for _, b := range read {
		exclude(b.Title)
		lowRated := b.UserRating >= 1 && b.UserRating <= 2
		trackSeries(b.Title, true, lowRated)

		author := normalizeAuthorName(b.Author)
		if author == "" {
			continue
		}
		key := strings.ToLower(author)
		display[key] = author
		switch {
		case b.UserRating >= 4:
			weights[key] += 2
			finished[key]++
		case b.UserRating == 3:
			weights[key] += 0.5
			finished[key]++
		case lowRated:
			weights[key] -= 2
		default: // finished, unrated
			weights[key]++
			finished[key]++
		}
	}
	for _, b := range want {
		exclude(b.Title)
		trackSeries(b.Title, false, false)
	}

	p := &RecProfile{Excluded: excluded}

	for key, w := range weights {
		if w >= minAuthorWeight {
			p.Authors = append(p.Authors, RecAuthor{Name: display[key], Weight: w, Finished: finished[key]})
		}
	}
	sort.Slice(p.Authors, func(i, j int) bool {
		if p.Authors[i].Weight != p.Authors[j].Weight {
			return p.Authors[i].Weight > p.Authors[j].Weight
		}
		return p.Authors[i].Name < p.Authors[j].Name
	})

	for key, sp := range series {
		if sp.MaxRead == 0 || seriesSuppressed[key] {
			continue
		}
		sort.Ints(sp.Shelved)
		p.Series = append(p.Series, *sp)
	}
	sort.Slice(p.Series, func(i, j int) bool { return p.Series[i].Name < p.Series[j].Name })

	return p
}
