package handlers

import (
	"net/http"
	"strings"
)

// StaticHandler serves files from dir with appropriate Cache-Control headers.
//   - *.html and /: no-cache (always re-validate)
//   - *.css, *.js: 1 hour (bump to immutable once asset hashing is added)
//   - fonts, images: 24 hours
func StaticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/" || strings.HasSuffix(p, ".html"):
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".js"):
			w.Header().Set("Cache-Control", "public, max-age=3600")
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
