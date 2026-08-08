package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/wbollock/benreadin/internal/db"
	"github.com/wbollock/benreadin/internal/handlers"
	appmw "github.com/wbollock/benreadin/internal/middleware"
	"github.com/wbollock/benreadin/internal/services"
)

// version is set at build time via -ldflags "-X main.version=<git-sha>".
var version = "dev"

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "run DB migrations and exit")
	flag.Parse()

	_ = godotenv.Load()

	cfg := loadConfig()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	database, err := db.Open(cfg.dbPath, cfg.libraryTTL, cfg.amazonTTL, cfg.bookTTL, cfg.shelfTTL, cfg.recProfileTTL)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	slog.Info("database ready", "path", cfg.dbPath)

	if *migrateOnly {
		slog.Info("migration complete, exiting")
		return
	}

	cache := services.NewCacheService(database, cfg.libraryTTL, cfg.amazonTTL, cfg.bookTTL, cfg.shelfTTL, cfg.recProfileTTL)

	goodreadsSvc := services.NewGoodreadsService(cache)
	overdriveSvc := services.NewOverDriveService(cache)
	openLibrarySvc := services.NewOpenLibraryService()
	gutenbergSvc := services.NewGutenbergService(database)

	go func() {
		if err := gutenbergSvc.LoadCatalog(context.Background()); err != nil {
			slog.Warn("gutenberg catalog load failed", "err", err)
		}
	}()

	amazonSvc := services.NewAmazonService(services.AmazonConfig{
		AccessKey:   cfg.amazonAccessKey,
		SecretKey:   cfg.amazonSecretKey,
		PartnerTag:  cfg.amazonPartnerTag,
		Marketplace: cfg.amazonMarketplace,
	}, cache)

	if amazonSvc.Enabled() {
		slog.Info("amazon PA-API enabled", "marketplace", cfg.amazonMarketplace)
	} else {
		slog.Info("amazon PA-API disabled (no credentials)")
	}

	recommendationSvc := services.NewRecommendationService(goodreadsSvc, overdriveSvc, openLibrarySvc, gutenbergSvc, amazonSvc, cache, cfg.recMax, cfg.recMaxAuthors, cfg.odConcurrency)

	// Background prewarm: periodically re-run seeded + recently used searches so
	// the book cache is warm before returning users search again.
	prewarmCtx, prewarmCancel := context.WithCancel(context.Background())
	defer prewarmCancel()
	if cfg.prewarmEnabled {
		prewarmSvc := services.NewPrewarmService(
			goodreadsSvc,
			overdriveSvc,
			openLibrarySvc,
			amazonSvc,
			gutenbergSvc,
			cache,
			services.ParsePrewarmSeeds(cfg.prewarmSeeds),
			time.Duration(cfg.prewarmIntervalSec)*time.Second,
			time.Duration(cfg.prewarmActiveDays)*24*time.Hour,
			cfg.bookTTL,
		)
		go prewarmSvc.Start(prewarmCtx)
		slog.Info("prewarm scheduler enabled",
			"interval_seconds", cfg.prewarmIntervalSec,
			"active_days", cfg.prewarmActiveDays,
			"seeds", cfg.prewarmSeeds)
	} else {
		slog.Info("prewarm scheduler disabled")
	}

	searchHandler := handlers.NewSearchHandler(
		goodreadsSvc,
		overdriveSvc,
		openLibrarySvc,
		amazonSvc,
		recommendationSvc,
		gutenbergSvc,
		cache,
		cfg.odConcurrency,
		cfg.odPerSearch,
		cfg.azConcurrency,
	)
	librariesHandler := handlers.NewLibrariesHandler(database)
	shortenHandler := handlers.NewShortenHandler(database)
	recsHandler := handlers.NewRecommendationsHandler(recommendationSvc)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(appmw.Logger)
	r.Use(appmw.Security)

	// JSON/redirect endpoints: compress responses, apply rate limits.
	r.With(chimw.Compress(5), httprate.LimitByIP(60, time.Minute)).Get("/api/libraries", librariesHandler.ServeHTTP)
	r.With(chimw.Compress(5), httprate.LimitByIP(30, time.Minute)).Post("/api/shorten", shortenHandler.Create)
	r.With(chimw.Compress(5)).Get("/s/{token}", shortenHandler.Redirect)

	// SSE: never compress; apply a request-rate limit per IP.
	r.With(httprate.LimitByIP(10, time.Minute)).Get("/api/search", searchHandler.ServeHTTP)
	r.With(httprate.LimitByIP(5, time.Minute)).Get("/api/recommendations", recsHandler.ServeHTTP)

	// Prometheus metrics. Not rate-limited or compressed; if the instance is
	// public, restrict this path at the reverse proxy.
	r.Handle("/metrics", promhttp.Handler())

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":%q,"amazon":%v,"gutenberg":%v}`+"\n",
			version, amazonSvc.Enabled(), gutenbergSvc.CatalogLoaded())
	})

	// Static files: compress HTML/CSS/JS, set cache headers.
	r.With(chimw.Compress(5)).Handle("/*", handlers.StaticHandler("public"))

	addr := ":" + cfg.port
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE streams are long-lived
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server starting", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("shutting down…")
	prewarmCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
	slog.Info("bye")
}

type config struct {
	port              string
	dbPath            string
	libraryTTL        int64
	amazonTTL         int64
	bookTTL           int64
	shelfTTL          int64
	odConcurrency     int64
	odPerSearch       int64
	azConcurrency     int64
	amazonAccessKey   string
	amazonSecretKey   string
	amazonPartnerTag  string
	amazonMarketplace string

	recProfileTTL int64
	recMax        int
	recMaxAuthors int

	prewarmEnabled     bool
	prewarmIntervalSec int64
	prewarmActiveDays  int64
	prewarmSeeds       string
}

func loadConfig() config {
	return config{
		port:              envOrDefault("PORT", "3000"),
		dbPath:            envOrDefault("DB_PATH", "./data/cache.db"),
		libraryTTL:        envInt64("CACHE_TTL_LIBRARY_SECONDS", 7200),
		amazonTTL:         envInt64("CACHE_TTL_AMAZON_SECONDS", 86400),
		bookTTL:           envInt64("CACHE_TTL_BOOK_SECONDS", 7200),
		shelfTTL:          envInt64("CACHE_TTL_SHELF_SECONDS", 300),
		odConcurrency:     envInt64("CONCURRENCY_OVERDRIVE", 50),
		odPerSearch:       envInt64("CONCURRENCY_OVERDRIVE_PER_SEARCH", 16),
		azConcurrency:     envInt64("CONCURRENCY_AMAZON", 3),
		amazonAccessKey:   os.Getenv("AMAZON_ACCESS_KEY"),
		amazonSecretKey:   os.Getenv("AMAZON_SECRET_KEY"),
		amazonPartnerTag:  os.Getenv("AMAZON_PARTNER_TAG"),
		amazonMarketplace: envOrDefault("AMAZON_MARKETPLACE", "www.amazon.com"),

		recProfileTTL: envInt64("REC_PROFILE_TTL_SECONDS", 86400),
		recMax:        int(envInt64("REC_MAX", 15)),
		recMaxAuthors: int(envInt64("REC_MAX_AUTHORS", 12)),

		prewarmEnabled:     envOrDefault("PREWARM_ENABLED", "true") == "true",
		prewarmIntervalSec: envInt64("PREWARM_INTERVAL_SECONDS", 3600),
		prewarmActiveDays:  envInt64("PREWARM_ACTIVE_DAYS", 10),
		// Format: "userID:shelf:lib1,lib2;userID2:shelf:lib3". Default keeps the
		// site owner's to-read shelf warm at the Free Library of Philadelphia.
		prewarmSeeds: envOrDefault("PREWARM_SEEDS", "97106512:to-read:freelibrary"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
