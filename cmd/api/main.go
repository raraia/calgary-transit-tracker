// Command api serves the on-time statistics API and the dashboard.
//
// Run:  go run ./cmd/api      then open http://localhost:8080
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // see cmd/collector for why

	"github.com/raraia/calgary-transit-tracker/internal/api"
	"github.com/raraia/calgary-transit-tracker/internal/store"
	"github.com/raraia/calgary-transit-tracker/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	dbURL := env("DATABASE_URL", "postgres://ctt:ctt@localhost:5432/ctt")
	addr := env("API_ADDR", ":8080")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, dbURL)
	if err != nil {
		log.Error("connecting to database", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Error("migrating database", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: api.New(st, log).Handler(web.Files),
		// Timeouts stop slow or stalled clients from holding connections open
		// forever, which is the basis of "slowloris" denial-of-service attacks.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Serve in the background so main can wait for Ctrl+C below.
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	// Graceful shutdown: stop accepting new connections and give in-flight
	// requests up to 10 seconds to finish before exiting.
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
