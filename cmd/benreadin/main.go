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
	"github.com/joho/godotenv"
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

	database, err := db.Open(cfg.dbPath, cfg.libraryTTL, cfg.amazonTTL, cfg.bookTTL)
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

	cache := services.NewCacheService(database, cfg.libraryTTL, cfg.amazonTTL, cfg.bookTTL)

	goodreadsSvc := services.NewGoodreadsService()
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

	recommendationSvc := services.NewRecommendationService(overdriveSvc)

	searchHandler := handlers.NewSearchHandler(
		goodreadsSvc,
		overdriveSvc,
		openLibrarySvc,
		amazonSvc,
		recommendationSvc,
		gutenbergSvc,
		cache,
		cfg.odConcurrency,
		cfg.azConcurrency,
	)
	librariesHandler := handlers.NewLibrariesHandler(database)
	shortenHandler := handlers.NewShortenHandler(database)
	recsHandler := handlers.NewRecommendationsHandler(goodreadsSvc, recommendationSvc)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(appmw.Logger)
	r.Use(appmw.Security)

	// JSON/redirect endpoints: compress responses.
	r.With(chimw.Compress(5)).Get("/api/recommendations", recsHandler.ServeHTTP)
	r.With(chimw.Compress(5)).Get("/api/libraries", librariesHandler.ServeHTTP)
	r.With(chimw.Compress(5)).Post("/api/shorten", shortenHandler.Create)
	r.With(chimw.Compress(5)).Get("/s/{token}", shortenHandler.Redirect)

	// SSE: never compress — streaming requires unfragmented chunks.
	r.Get("/api/search", searchHandler.ServeHTTP)

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
		bookTTL:           envInt64("CACHE_TTL_BOOK_SECONDS", 7200),
		odConcurrency:     envInt64("CONCURRENCY_OVERDRIVE", 50),
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
