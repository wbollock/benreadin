package models

import "testing"

func TestSearchTitle(t *testing.T) {
	tests := []struct {
		title, want string
	}{
		{"The Gunslinger (The Dark Tower, #1)", "The Gunslinger"},
		{"Harry Potter and the Sorcerer's Stone (Harry Potter, #1)", "Harry Potter and the Sorcerer's Stone"},
		{"The Fellowship of the Ring (The Lord of the Rings, #1-3)", "The Fellowship of the Ring"},
		{"The Sandman (Vertigo) (The Sandman #1)", "The Sandman (Vertigo)"},
		// Parentheticals without "#" are part of the title, not series noise.
		{"Frankenstein (Illustrated Edition)", "Frankenstein (Illustrated Edition)"},
		{"Educated", "Educated"},
		{"", ""},
	}
	for _, tt := range tests {
		b := Book{Title: tt.title}
		if got := b.SearchTitle(); got != tt.want {
			t.Errorf("SearchTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}
