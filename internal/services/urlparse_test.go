package services

import (
	"testing"
)

func TestParseShelfURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantUserID  string
		wantShelf   string
		wantLibs    []string
		wantErrFrag string // non-empty means we expect an error containing this string
	}{
		// Bare numeric ID
		{
			name:       "bare numeric ID",
			input:      "12345678",
			wantUserID: "12345678",
			wantShelf:  "to-read",
		},
		// Bare ID with slug suffix (as copied from a profile URL)
		{
			name:       "bare ID with slug",
			input:      "97106512-william",
			wantUserID: "97106512",
			wantShelf:  "to-read",
		},
		// Goodreads profile URL with slug
		{
			name:       "profile URL with slug",
			input:      "https://www.goodreads.com/user/show/97106512-william",
			wantUserID: "97106512",
			wantShelf:  "to-read",
		},
		// Goodreads profile URL without slug
		{
			name:       "profile URL no slug",
			input:      "https://www.goodreads.com/user/show/12345678",
			wantUserID: "12345678",
			wantShelf:  "to-read",
		},
		// Goodreads shelf URL
		{
			name:       "shelf URL with explicit shelf",
			input:      "https://www.goodreads.com/review/list/12345678?shelf=read",
			wantUserID: "12345678",
			wantShelf:  "read",
		},
		// Goodreads shelf URL defaults to to-read
		{
			name:       "shelf URL no shelf param",
			input:      "https://www.goodreads.com/review/list/12345678",
			wantUserID: "12345678",
			wantShelf:  "to-read",
		},
		// Goodreads without www
		{
			name:       "goodreads no www",
			input:      "https://goodreads.com/user/show/12345678",
			wantUserID: "12345678",
			wantShelf:  "to-read",
		},
		// OverReader URL
		{
			name:       "overreader URL single library",
			input:      "https://overreader.com/overdrive/nypl/goodreads/12345678/shelf/to-read",
			wantUserID: "12345678",
			wantShelf:  "to-read",
			wantLibs:   []string{"nypl"},
		},
		// OverReader URL with multiple libraries
		{
			name:       "overreader URL multi-library",
			input:      "https://overreader.com/overdrive/nypl,bklynlib/goodreads/12345678/shelf/to-read",
			wantUserID: "12345678",
			wantShelf:  "to-read",
			wantLibs:   []string{"nypl", "bklynlib"},
		},
		// Whitespace trimmed
		{
			name:       "whitespace trimmed",
			input:      "  12345678  ",
			wantUserID: "12345678",
			wantShelf:  "to-read",
		},
		// Error: unsupported host
		{
			name:        "unsupported host",
			input:       "https://example.com/user/123",
			wantErrFrag: "unsupported URL host",
		},
		// Error: OverReader without library keys
		{
			name:        "overreader no library keys",
			input:       "https://overreader.com/overdrive//goodreads/12345678/shelf/to-read",
			wantErrFrag: "no library keys",
		},
		// Error: unrecognised Goodreads path
		{
			name:        "bad goodreads path",
			input:       "https://www.goodreads.com/book/show/12345",
			wantErrFrag: "unrecognised Goodreads URL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShelfURL(tc.input)
			if tc.wantErrFrag != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrFrag)
				}
				if !contains(err.Error(), tc.wantErrFrag) {
					t.Fatalf("expected error %q to contain %q", err.Error(), tc.wantErrFrag)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.UserID != tc.wantUserID {
				t.Errorf("UserID = %q, want %q", got.UserID, tc.wantUserID)
			}
			if got.Shelf != tc.wantShelf {
				t.Errorf("Shelf = %q, want %q", got.Shelf, tc.wantShelf)
			}
			if tc.wantLibs != nil {
				if len(got.Libraries) != len(tc.wantLibs) {
					t.Fatalf("Libraries = %v, want %v", got.Libraries, tc.wantLibs)
				}
				for i, lib := range tc.wantLibs {
					if got.Libraries[i] != lib {
						t.Errorf("Libraries[%d] = %q, want %q", i, got.Libraries[i], lib)
					}
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
