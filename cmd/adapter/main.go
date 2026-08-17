// Command adapter serves the Agent Memory Leaderboard Add/Search contract
// backed by a demarkus server.
package main

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/api"
	"github.com/latebit-io/memoryleaderboard/internal/llmwiring"
	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

// searchBudget bounds a full agentic Search; it sizes both the response
// write timeout and the graceful-shutdown window.
const searchBudget = 120 * time.Second

func env(key, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
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

	// ADAPTER_NAV selects the search strategy:
	//   auto (default) agentic search, degrading to catalog lookup
	//   off            catalog lookup only
	//   require        agentic search with no silent degradation
	navMode := env("ADAPTER_NAV", "auto")
	switch navMode {
	case "auto", "off", "require":
	default:
		logger.Error("invalid ADAPTER_NAV value", "value", navMode)
		os.Exit(1)
	}
	distillMode := env("ADAPTER_DISTILL", "auto")
	switch distillMode {
	case "auto", "off", "require":
	default:
		logger.Error("invalid ADAPTER_DISTILL value", "value", distillMode)
		os.Exit(1)
	}
	provider := llmwiring.Provider(logger)

	base := memory.NewDemarkusStore(demarkusHost, demarkusToken, insecure)
	defer base.Close()
	if provider != nil && distillMode != "off" {
		base.SetDistiller(memory.NewLLMDistiller(provider, 30*time.Second))
	} else if distillMode == "require" {
		logger.Error("ADAPTER_DISTILL=require but no LLM provider is configured")
		os.Exit(1)
	}

	var store memory.Store = base
	navEnabled := false
	if navMode != "off" {
		switch {
		case provider != nil:
			// Leave headroom under WriteTimeout so a search that exhausts
			// its budget can still fall back and answer.
			nav := memory.NewNavStore(base, base, provider, memory.NavOptions{Budget: searchBudget * 3 / 4})
			navEnabled = true
			if navMode == "require" {
				store = nav
			} else {
				store = memory.WithFallback(nav, base)
			}
		case navMode == "require":
			logger.Error("ADAPTER_NAV=require but no LLM provider is configured")
			os.Exit(1)
		default:
			logger.Warn("no LLM provider configured; Search runs catalog lookup only")
		}
	}

	e := echo.New()
	api.Routes(e, api.NewHandler(store), api.Config{
		APIKey: apiKey, NavEnabled: navEnabled, DistillEnabled: base.DistillationEnabled(),
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      searchBudget,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("adapter listening", "addr", addr, "demarkus", demarkusHost)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		// ListenAndServe only returns pre-shutdown on a listener failure.
		logger.Error("server failed", "err", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	// Same budget as WriteTimeout so an in-flight Search can finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), searchBudget)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("forced shutdown", "err", err)
		os.Exit(1)
	}
}
