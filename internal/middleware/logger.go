package middleware

import (
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// Logger is a structured request logger built on log/slog.
// It wraps chi's WrapResponseWriter so it captures status code and bytes written.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		reqID := chimw.GetReqID(r.Context())
		defer func() {
			slog.Info("request",
				"id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"ms", time.Since(start).Milliseconds(),
				"ip", r.RemoteAddr,
			)
		}()
		next.ServeHTTP(ww, r)
	})
}
