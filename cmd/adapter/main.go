// Command adapter serves the Agent Memory Leaderboard Add/Search contract
// backed by a demarkus server.
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

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/api"
	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	addr := env("ADAPTER_ADDR", ":8080")
	apiKey := os.Getenv("ADAPTER_API_KEY")
	if apiKey == "" && os.Getenv("ADAPTER_ALLOW_UNAUTHENTICATED") != "1" {
		logger.Error("ADAPTER_API_KEY is required; set ADAPTER_ALLOW_UNAUTHENTICATED=1 to run open (dev only)")
		os.Exit(1)
	}
	demarkusHost := env("DEMARKUS_HOST", "localhost:6309")
	demarkusToken := os.Getenv("DEMARKUS_TOKEN")
	insecure := os.Getenv("DEMARKUS_INSECURE") == "1"

	store := memory.NewDemarkusStore(demarkusHost, demarkusToken, insecure)
	defer store.Close()

	e := echo.New()
	api.Routes(e, api.NewHandler(store), apiKey)

	// WriteTimeout must cover a full agentic Search, which costs seconds.
	server := &http.Server{
		Addr:              addr,
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("adapter listening", "addr", addr, "demarkus", demarkusHost)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
