// Package metrics defines the Prometheus instrumentation for benreadin,
// exposed at /metrics. The set is deliberately small: enough to watch cache
// effectiveness, upstream health, and load once more than one user is on the
// instance.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// SearchesTotal counts SSE search streams by outcome: "ok" (reached done),
	// "error" (bad input or upstream failure), "canceled" (client went away).
	SearchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "benreadin_searches_total",
		Help: "Search streams finished, by outcome.",
	}, []string{"outcome"})

	// BooksCheckedTotal counts books processed by search streams, split by
	// whether the full result came from the book cache.
	BooksCheckedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "benreadin_books_checked_total",
		Help: "Books processed by search streams, by source.",
	}, []string{"source"}) // cache | fetched

	// CacheRequestsTotal tracks SQLite cache lookups by cache and result.
	CacheRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "benreadin_cache_requests_total",
		Help: "Cache lookups, by cache name and hit/miss.",
	}, []string{"cache", "result"})

	// UpstreamErrorsTotal counts failed calls to external services.
	UpstreamErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "benreadin_upstream_errors_total",
		Help: "Failed requests to upstream services.",
	}, []string{"service"}) // goodreads | overdrive | amazon

	// ActiveStreams is the number of SSE search streams currently open.
	ActiveStreams = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "benreadin_active_streams",
		Help: "Currently open SSE search streams.",
	})

	// ShelfFetchDuration times uncached Goodreads shelf fetches (all pages).
	ShelfFetchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "benreadin_shelf_fetch_duration_seconds",
		Help:    "Duration of uncached Goodreads shelf fetches.",
		Buckets: prometheus.DefBuckets,
	})

	// RecsGeneratedTotal counts recommendations emitted, by source engine.
	RecsGeneratedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "benreadin_recs_generated_total",
		Help: "Recommendations generated, by source.",
	}, []string{"source"}) // series | author | subject
)
