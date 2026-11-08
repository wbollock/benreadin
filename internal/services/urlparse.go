package services

import (
	"fmt"
	"net/url"
	"strings"
)

// ParsedShelfURL contains everything extracted from a Hardcover, Goodreads, or OverReader URL.
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
//	https://hardcover.app/@{user}[/books[/{status}]]       (Hardcover)
//	bare "@user" Hardcover username
//
// Hardcover users come back with UserID "@user" — that prefix is how the rest
// of the app (ShelfRouter, caches, prewarm seeds) tells the sources apart.
func ParseShelfURL(raw string) (*ParsedShelfURL, error) {
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "@") {
		user := raw[1:]
		if !validHardcoverUsername(user) {
			return nil, fmt.Errorf("%q isn't a valid Hardcover username", raw)
		}
		return &ParsedShelfURL{UserID: "@" + strings.ToLower(user), Shelf: "to-read"}, nil
	}

	// Scheme-less paste ("hardcover.app/@you") — url.Parse would treat the
	// whole thing as a path.
	if strings.HasPrefix(raw, "hardcover.app/") || strings.HasPrefix(raw, "www.hardcover.app/") {
		raw = "https://" + raw
	}

	// Bare Goodreads user ID, optionally with a "-slug" suffix as it appears in
	// profile URLs (e.g. "97106512" or "97106512-william"). Default to to-read.
	if id := bareGoodreadsID(raw); id != "" {
		return &ParsedShelfURL{UserID: id, Shelf: "to-read"}, nil
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
	case "hardcover.app", "www.hardcover.app":
		return parseHardcoverURL(u)
	default:
		return nil, fmt.Errorf("benreadin doesn't know links from %q. Enter your Hardcover username, like @yourname", u.Host)
	}
}

// bareGoodreadsID extracts the numeric user ID from a bare (non-URL) input that
// is either all digits ("97106512") or the digits-plus-slug form found in
// profile URLs ("97106512-william"). It returns "" when raw contains path or
// host separators, so genuine URLs fall through to url.Parse.
func bareGoodreadsID(raw string) string {
	if strings.ContainsAny(raw, "/:. ") {
		return ""
	}
	id := raw
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		id = raw[:i]
	}
	if isNumericID(id) {
		return id
	}
	return ""
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
		return nil, fmt.Errorf("that doesn't look like a Goodreads profile or shelf link")
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

// parseHardcoverURL parses:
//
//	/@{user}                       (profile → to-read)
//	/@{user}/books                 (library → to-read)
//	/@{user}/books/{status}        (want-to-read | currently-reading | read | did-not-finish)
func parseHardcoverURL(u *url.URL) (*ParsedShelfURL, error) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "@") || !validHardcoverUsername(parts[0][1:]) {
		return nil, fmt.Errorf("that doesn't look like a Hardcover profile link. Try hardcover.app/@yourname")
	}
	shelf := "to-read"
	if len(parts) >= 3 && parts[1] == "books" && parts[2] != "" {
		shelf = HardcoverShelfName(parts[2])
		if _, ok := hardcoverStatusIDs[shelf]; !ok {
			return nil, fmt.Errorf("Hardcover doesn't have a %q list. Use want-to-read, currently-reading, read or did-not-finish", parts[2])
		}
	}
	return &ParsedShelfURL{UserID: "@" + strings.ToLower(parts[0][1:]), Shelf: shelf}, nil
}

// validHardcoverUsername allows the characters Hardcover usernames use.
func validHardcoverUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
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
