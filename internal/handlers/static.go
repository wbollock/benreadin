package handlers

import (
	"net/http"
	"strings"
)

// StaticHandler serves files from dir with appropriate Cache-Control headers.
//   - *.html and /: no-cache (always re-validate)
//   - *.css, *.js: no-cache (always re-validate) — HTML references these by a
//     fixed path, so a stale cached copy can be paired with freshly-fetched HTML
//     and break the page. Revalidation is cheap (304 via ETag/Last-Modified);
//     switch to immutable + long max-age once filenames are content-hashed.
//   - fonts, images: 24 hours
func StaticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/" || strings.HasSuffix(p, ".html"):
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".js"):
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(p, ".woff2") || strings.HasSuffix(p, ".woff") ||
			strings.HasSuffix(p, ".png") || strings.HasSuffix(p, ".ico") ||
			strings.HasSuffix(p, ".svg"):
			w.Header().Set("Cache-Control", "public, max-age=86400")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}
