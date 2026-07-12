package services

import (
	"reflect"
	"testing"
)

func TestParsePrewarmSeeds(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []PrewarmTarget
	}{
		{
			name: "default seed",
			raw:  "97106512:to-read:freelibrary",
			want: []PrewarmTarget{{UserID: "97106512", Shelf: "to-read", Libraries: []string{"freelibrary"}}},
		},
		{
			name: "multiple seeds and libraries",
			raw:  "1:to-read:nypl,bklynlib; 2:read:lapl",
			want: []PrewarmTarget{
				{UserID: "1", Shelf: "to-read", Libraries: []string{"nypl", "bklynlib"}},
				{UserID: "2", Shelf: "read", Libraries: []string{"lapl"}},
			},
		},
		{
			name: "empty shelf defaults to to-read",
			raw:  "1::nypl",
			want: []PrewarmTarget{{UserID: "1", Shelf: "to-read", Libraries: []string{"nypl"}}},
		},
		{
			name: "malformed entries skipped",
			raw:  "banana;:shelf:lib;1:shelf:;;3:to-read:nypl",
			want: []PrewarmTarget{{UserID: "3", Shelf: "to-read", Libraries: []string{"nypl"}}},
		},
		{
			name: "empty input",
			raw:  "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParsePrewarmSeeds(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParsePrewarmSeeds(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLibrariesKey(t *testing.T) {
	if got := LibrariesKey([]string{"nypl", "bklynlib", "lapl"}); got != "bklynlib,lapl,nypl" {
		t.Errorf("LibrariesKey = %q, want sorted key", got)
	}
	in := []string{"b", "a"}
	LibrariesKey(in)
	if in[0] != "b" {
		t.Error("LibrariesKey must not mutate its input")
	}
}
