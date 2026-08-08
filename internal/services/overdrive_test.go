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
