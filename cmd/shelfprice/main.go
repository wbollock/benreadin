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
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/wbollock/shelfprice/internal/db"
	"github.com/wbollock/shelfprice/internal/handlers"
	"github.com/wbollock/shelfprice/internal/services"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "run DB migrations and exit")
	flag.Parse()

	// Load .env if present (ignore error — env vars may be set externally)
	_ = godotenv.Load()

	cfg := loadConfig()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Open database
	database, err := db.Open(cfg.dbPath, cfg.libraryTTL, cfg.amazonTTL)
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

	// Wire up services
	cache := services.NewCacheService(database, cfg.libraryTTL, cfg.amazonTTL)

	goodreadsSvc := services.NewGoodreadsService()
	overdriveSvc := services.NewOverDriveService(cache)
	openLibrarySvc := services.NewOpenLibraryService()
	amazonSvc := services.NewAmazonService(services.AmazonConfig{
		AccessKey:   cfg.amazonAccessKey,
		SecretKey:   cfg.amazonSecretKey,
		PartnerTag:  cfg.amazonPartnerTag,
		Marketplace: cfg.amazonMarketplace,
	}, cache)

	if amazonSvc.Enabled() {
		slog.Info("amazon PA-API enabled", "marketplace", cfg.amazonMarketplace)
	} else {
		slog.Info("amazon PA-API disabled (no credentials) — prices will not be shown")
	}

	recommendationSvc := services.NewRecommendationService(overdriveSvc)

	// Wire up handlers
	searchHandler := handlers.NewSearchHandler(
		goodreadsSvc,
		overdriveSvc,
		openLibrarySvc,
		amazonSvc,
		recommendationSvc,
		cfg.odConcurrency,
		cfg.azConcurrency,
	)
	librariesHandler := handlers.NewLibrariesHandler(database)
	shortenHandler := handlers.NewShortenHandler(database)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// API routes
	r.Get("/api/search", searchHandler.ServeHTTP)
	r.Get("/api/libraries", librariesHandler.ServeHTTP)
	r.Post("/api/shorten", shortenHandler.Create)

	// Shortlink redirects
	r.Get("/s/{token}", shortenHandler.Redirect)

	// Health check
	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// Static files
	r.Handle("/*", http.FileServer(http.Dir("public")))

	addr := ":" + cfg.port
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second, // long for SSE streams
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("shutting down...")

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
	odConcurrency     int64
	azConcurrency     int64
	amazonAccessKey   string
	amazonSecretKey   string
	amazonPartnerTag  string
	amazonMarketplace string
}

func loadConfig() config {
	return config{
		port:              envOrDefault("PORT", "3000"),
		dbPath:            envOrDefault("DB_PATH", "./data/cache.db"),
		libraryTTL:        envInt64("CACHE_TTL_LIBRARY_SECONDS", 7200),
		amazonTTL:         envInt64("CACHE_TTL_AMAZON_SECONDS", 86400),
		odConcurrency:     envInt64("CONCURRENCY_OVERDRIVE", 5),
		azConcurrency:     envInt64("CONCURRENCY_AMAZON", 3),
		amazonAccessKey:   os.Getenv("AMAZON_ACCESS_KEY"),
		amazonSecretKey:   os.Getenv("AMAZON_SECRET_KEY"),
		amazonPartnerTag:  os.Getenv("AMAZON_PARTNER_TAG"),
		amazonMarketplace: envOrDefault("AMAZON_MARKETPLACE", "www.amazon.com"),
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
