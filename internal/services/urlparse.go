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

// ParseShelfURL accepts either:
//
//	https://overreader.com/overdrive/{libraries}/{source}/{userId}/shelf/{shelf}?lookfor=e,a
//	https://www.goodreads.com/review/list/{USER_ID}?shelf={SHELF}
func ParseShelfURL(raw string) (*ParsedShelfURL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	switch u.Host {
	case "overreader.com", "www.overreader.com":
		return parseOverReaderURL(u)
	case "www.goodreads.com", "goodreads.com":
		return parseGoodreadsURL(u)
	default:
		return nil, fmt.Errorf("unsupported URL host %q — paste an OverReader or Goodreads shelf URL", u.Host)
	}
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
// /review/list/{USER_ID}?shelf={SHELF}
func parseGoodreadsURL(u *url.URL) (*ParsedShelfURL, error) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	// Expected: ["review", "list", userId]
	if len(parts) < 3 || parts[0] != "review" || parts[1] != "list" {
		return nil, fmt.Errorf("unrecognised Goodreads URL path: %s", u.Path)
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
