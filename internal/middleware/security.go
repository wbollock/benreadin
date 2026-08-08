package middleware

import "net/http"

// Security sets hardening HTTP headers on every response.
// Inline styles/scripts are permitted until Phase 2 removes them; tighten
// the CSP after all inline style="" attributes are moved to app.css.
func Security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' https://i.gr-assets.com https://images-na.ssl-images-amazon.com "+
				"https://covers.openlibrary.org https://*.od-cdn.com data:; "+
				"style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"font-src 'self'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'",
		)
		// HSTS only when the request arrived over HTTPS.
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
