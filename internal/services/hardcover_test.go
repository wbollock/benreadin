package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseShelfURLHardcover(t *testing.T) {
	tests := []struct {
		input, wantUser, wantShelf, wantErr string
	}{
		{input: "@wbollock", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "  @WBollock ", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "https://hardcover.app/@wbollock", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "hardcover.app/@wbollock", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "https://www.hardcover.app/@wbollock/", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "https://hardcover.app/@wbollock/books", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "https://hardcover.app/@wbollock/books/want-to-read", wantUser: "@wbollock", wantShelf: "to-read"},
		{input: "https://hardcover.app/@wbollock/books/currently-reading", wantUser: "@wbollock", wantShelf: "currently-reading"},
		{input: "https://hardcover.app/@wbollock/books/read?x=1", wantUser: "@wbollock", wantShelf: "read"},
		{input: "https://hardcover.app/@wbollock/books/did-not-finish", wantUser: "@wbollock", wantShelf: "did-not-finish"},
		{input: "https://hardcover.app/@wbollock/books/owned", wantErr: "unsupported Hardcover shelf"},
		{input: "https://hardcover.app/books/dune", wantErr: "unrecognised Hardcover URL"},
		{input: "@bad name", wantErr: "invalid Hardcover username"},
		{input: "@", wantErr: "invalid Hardcover username"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseShelfURL(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.UserID != tt.wantUser || got.Shelf != tt.wantShelf {
				t.Fatalf("got (%q, %q), want (%q, %q)", got.UserID, got.Shelf, tt.wantUser, tt.wantShelf)
			}
		})
	}
}

const hcFixture = `{"data":{"users":[{"id":1}],"user_books":[
 {"date_added":"2026-06-17","rating":4.5,
  "edition":{"isbn_10":"0316037702","isbn_13":"9780316037709","pages":413,"image":{"url":"https://assets.hardcover.app/e.jpeg"}},
  "book":{"id":386744,"slug":"ghost-in-the-wires","title":"Ghost in the Wires","description":"<p>A <b>hacker</b> memoir.</p>",
   "pages":400,"rating":3.987654,"ratings_count":10,"image":{"url":"https://assets.hardcover.app/b.jpeg"},
   "contributions":[{"contribution":"Foreword","author":{"name":"Steve Wozniak"}},{"contribution":null,"author":{"name":"Kevin   Mitnick"}}]}},
 {"date_added":null,"rating":null,"edition":null,
  "book":{"id":2,"slug":"","title":"No Edition","description":null,"pages":0,"rating":0,"ratings_count":0,"image":{"url":"https://assets.hardcover.app/2.jpeg"},
   "contributions":[{"contribution":"Translator","author":{"name":"T. Ranslator"}}]}},
 {"date_added":"2026-01-01","rating":null,"edition":null,
  "book":{"id":2,"slug":"","title":"Duplicate","description":null,"pages":0,"rating":0,"ratings_count":0,"image":null,"contributions":[]}}
]}}`

func newTestHardcover(t *testing.T, handler http.HandlerFunc) *HardcoverService {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := NewHardcoverService("Bearer test-token", nil)
	s.endpoint = srv.URL
	return s
}

func TestHardcoverFetchShelf(t *testing.T) {
	var gotVars map[string]any
	s := newTestHardcover(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotVars = body.Variables
		io.WriteString(w, hcFixture)
	})

	books, err := s.FetchShelf(context.Background(), "@WBollock", "currently-reading", false)
	if err != nil {
		t.Fatal(err)
	}
	if gotVars["u"] != "wbollock" || gotVars["s"] != float64(2) {
		t.Errorf("variables = %v", gotVars)
	}
	if len(books) != 2 {
		t.Fatalf("got %d books (duplicates should be dropped), want 2", len(books))
	}
	b := books[0]
	if b.GoodreadsID != "hc-386744" || b.Source != "hardcover" || b.BookURL != "https://hardcover.app/books/ghost-in-the-wires" {
		t.Errorf("ids: %+v", b)
	}
	if b.Author != "Kevin Mitnick" {
		t.Errorf("Author = %q, want primary author with collapsed whitespace", b.Author)
	}
	if b.ISBN13 != "9780316037709" || b.ISBN10 != "0316037702" || b.PageCount != 413 {
		t.Errorf("edition fields: %+v", b)
	}
	if b.CoverURL != "https://assets.hardcover.app/e.jpeg" {
		t.Errorf("CoverURL = %q, want edition cover", b.CoverURL)
	}
	if b.Description != "A hacker memoir." || b.AverageRating != 3.99 || b.UserRating != 5 || b.DateAddedUnix == 0 {
		t.Errorf("misc fields: %+v", b)
	}
	nb := books[1]
	if nb.Author != "T. Ranslator" || nb.CoverURL != "https://assets.hardcover.app/2.jpeg" || nb.BookURL != "" || nb.UserRating != 0 {
		t.Errorf("fallbacks: %+v", nb)
	}
}

func TestHardcoverErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind string
	}{
		{"unknown user", 200, `{"data":{"users":[],"user_books":[]}}`, "not_found"},
		{"missing auth", 200, `{"error":"invalid_request","error_description":"No Authorization header"}`, "auth"},
		{"401", 401, `{}`, "auth"},
		{"rate limited", 429, ``, "rate_limited"},
		{"5xx", 502, ``, "unavailable"},
		{"graphql error", 200, `{"errors":[{"message":"field 'x' not found"}]}`, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestHardcover(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			_, err := s.FetchShelf(context.Background(), "someone", "to-read", false)
			var hcErr *HardcoverError
			if !errors.As(err, &hcErr) || hcErr.Kind != tt.wantKind {
				t.Fatalf("err = %#v, want kind %q", err, tt.wantKind)
			}
		})
	}
}

func TestHardcoverUnconfiguredAndBadShelf(t *testing.T) {
	var hcErr *HardcoverError
	if _, err := NewHardcoverService("", nil).FetchShelf(context.Background(), "x", "to-read", false); !errors.As(err, &hcErr) || hcErr.Kind != "unconfigured" {
		t.Errorf("unconfigured: %v", err)
	}
	if _, err := NewHardcoverService("t", nil).FetchShelf(context.Background(), "x", "sci-fi", false); !errors.As(err, &hcErr) || hcErr.Kind != "bad_shelf" {
		t.Errorf("bad shelf: %v", err)
	}
}

func TestShelfRouter(t *testing.T) {
	if !IsHardcoverUser("@x") || IsHardcoverUser("97106512") {
		t.Fatal("IsHardcoverUser")
	}
	r := &ShelfRouter{Goodreads: NewGoodreadsService(nil), Hardcover: NewHardcoverService("", nil)}
	var hcErr *HardcoverError
	if _, err := r.FetchShelf(context.Background(), "@x", "to-read", false); !errors.As(err, &hcErr) {
		t.Errorf("@user should route to Hardcover, got %v", err)
	}
}
