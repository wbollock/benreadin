package services

import (
	"fmt"
	"net/url"
	"strings"
)

// ParsedShelfURL contains everything extracted from a Goodreads or OverReader URL.
type ParsedShelfURL struct {
	UserID    string
	Shelf     string
	Libraries []string // OverDrive library keys
	LookFor   []string // e.g. ["e", "a"] for ebooks/audiobooks
}

// ParseShelfURL accepts:
//
//	https://overreader.com/overdrive/{libraries}/{source}/{userId}/shelf/{shelf}?lookfor=e,a
//	https://www.goodreads.com/review/list/{USER_ID}?shelf={SHELF}
//	https://www.goodreads.com/user/show/{USER_ID}[-slug]   (profile URL)
//	bare numeric Goodreads user ID (e.g. "12345678")
func ParseShelfURL(raw string) (*ParsedShelfURL, error) {
	raw = strings.TrimSpace(raw)

	// Bare numeric user ID — treat as Goodreads user ID, default to to-read shelf.
	if isNumericID(raw) {
		return &ParsedShelfURL{UserID: raw, Shelf: "to-read"}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	switch u.Host {
	case "overreader.com", "www.overreader.com":
		return parseOverReaderURL(u)
	case "www.goodreads.com", "goodreads.com":
		return parseGoodreadsURL(u)
	default:
		return nil, fmt.Errorf("unsupported URL host %q — paste a Goodreads shelf/profile URL or OverReader URL", u.Host)
	}
}

// isNumericID returns true if s is a non-empty string of digits only.
func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseOverReaderURL parses:
// /overdrive/{libraries}/{source}/{userId}/shelf/{shelf}
func parseOverReaderURL(u *url.URL) (*ParsedShelfURL, error) {
	// Strip leading slash and split
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	// Expected: ["overdrive", libraries, source, userId, "shelf", shelfName]
	if len(parts) < 6 || parts[0] != "overdrive" || parts[4] != "shelf" {
		return nil, fmt.Errorf("unrecognised OverReader URL path: %s", u.Path)
	}

	libs := splitComma(parts[1])
	if len(libs) == 0 {
		return nil, fmt.Errorf("no library keys found in OverReader URL")
	}

	result := &ParsedShelfURL{
		UserID:    parts[3],
		Shelf:     parts[5],
		Libraries: libs,
	}

	if lf := u.Query().Get("lookfor"); lf != "" {
		result.LookFor = splitComma(lf)
	}

	return result, nil
}

// parseGoodreadsURL parses:
//
//	/review/list/{USER_ID}?shelf={SHELF}          (shelf URL)
//	/user/show/{USER_ID}[-slug]                   (profile URL)
func parseGoodreadsURL(u *url.URL) (*ParsedShelfURL, error) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")

	// Profile URL: /user/show/{id} or /user/show/{id}-{slug}
	if len(parts) >= 3 && parts[0] == "user" && parts[1] == "show" {
		idPart := parts[2]
		// Strip optional "-username" suffix so "12345678-wbollock" → "12345678"
		if i := strings.IndexByte(idPart, '-'); i > 0 {
			idPart = idPart[:i]
		}
		if idPart == "" {
			return nil, fmt.Errorf("could not extract user ID from Goodreads profile URL")
		}
		return &ParsedShelfURL{UserID: idPart, Shelf: "to-read"}, nil
	}

	// Shelf URL: /review/list/{USER_ID}?shelf={SHELF}
	if len(parts) < 3 || parts[0] != "review" || parts[1] != "list" {
		return nil, fmt.Errorf("unrecognised Goodreads URL — paste your profile URL (goodreads.com/user/show/…) or shelf URL (goodreads.com/review/list/…)")
	}

	shelf := u.Query().Get("shelf")
	if shelf == "" {
		shelf = "to-read"
	}

	return &ParsedShelfURL{
		UserID: parts[2],
		Shelf:  shelf,
	}, nil
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
