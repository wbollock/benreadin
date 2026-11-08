package services

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// newHTTPClient returns a client tuned for many concurrent requests to a
// single API host. The default transport keeps only 2 idle connections per
// host, so a burst of concurrent calls (e.g. 50 parallel OverDrive checks)
// closes and re-opens TLS connections constantly; sizing the idle pool to the
// caller's concurrency lets every request reuse a warm connection.
//
// The transport is wrapped with otelhttp so every outbound call to
// OverDrive/Amazon/Open Library gets a client span as a child of whatever
// span is active on the request context.
func newHTTPClient(timeout time.Duration, maxIdlePerHost int) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = maxIdlePerHost * 2
	t.MaxIdleConnsPerHost = maxIdlePerHost
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(t)}
}
