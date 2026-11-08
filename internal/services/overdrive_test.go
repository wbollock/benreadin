package services

import "testing"

func TestTitlesMatch(t *testing.T) {
	tests := []struct {
		got, want string
		match     bool
	}{
		// The bug: Thunder's top hit for a Dark Tower query was a tie-in.
		{"Charlie the Choo-Choo", "The Gunslinger", false},
		{"The Gunslinger", "The Gunslinger", true},
		{"Gwendy's Magic Feather", "The Gunslinger", false},
		{"The Gunslinger Born", "The Gunslinger", false},
		// Subtitles on either side.
		{"The Gunslinger: The Dark Tower I", "The Gunslinger", true},
		{"The Dark Tower I: The Gunslinger", "The Gunslinger", true},
		{"Educated", "Educated: A Memoir", true},
		// Article and punctuation differences.
		{"Gunslinger", "The Gunslinger", true},
		{"The Handmaid's Tale", "The Handmaids Tale", true},
		{"", "The Gunslinger", false},
		{"The Gunslinger", "", false},
	}
	for _, tt := range tests {
		if got := titlesMatch(tt.got, tt.want); got != tt.match {
			t.Errorf("titlesMatch(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.match)
		}
	}
}

func TestEnglishOnly(t *testing.T) {
	mk := func(title string, langs ...string) thunderItem {
		it := thunderItem{Title: title}
		for _, l := range langs {
			it.Languages = append(it.Languages, struct {
				ID string `json:"id"`
			}{l})
		}
		return it
	}
	got := englishOnly([]thunderItem{mk("Carl", "en"), mk("El portal", "es"), mk("Untagged"), mk("Bilingual", "es", "EN")})
	var titles []string
	for _, it := range got {
		titles = append(titles, it.Title)
	}
	if len(titles) != 3 || titles[0] != "Carl" || titles[1] != "Untagged" || titles[2] != "Bilingual" {
		t.Fatalf("englishOnly kept %v", titles)
	}
}
