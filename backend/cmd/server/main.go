package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saitenntaisei/better-connected-paper/internal/api"
	"github.com/saitenntaisei/better-connected-paper/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	migrateOnly := flag.Bool("migrate-only", false, "apply migrations and exit")
	flag.Parse()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var db *store.DB
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		db, err = store.Open(openCtx, dsn)
		cancel()
		if err != nil {
			logger.Error("db open failed", "err", err)
			os.Exit(1)
		}
		defer db.Close()

		migrateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := db.Migrate(migrateCtx); err != nil {
			cancel()
			logger.Error("migrate failed", "err", err)
			os.Exit(1)
		}
		cancel()
		logger.Info("db migrations applied")
	} else if *migrateOnly {
		logger.Error("-migrate-only requires DATABASE_URL")
		os.Exit(1)
	} else {
		logger.Warn("DATABASE_URL not set; running without persistence cache")
	}

	if *migrateOnly {
		return
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("0.0.0.0", port),
		Handler:           api.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
