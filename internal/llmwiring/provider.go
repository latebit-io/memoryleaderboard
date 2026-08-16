// Package llmwiring resolves the LLM provider the agentic paths use.
// Kept out of main so tests and future commands share one credential
// cascade.
package llmwiring

import (
	"log/slog"
	"os"

	"github.com/latebit-io/nib/ai/llm"
	"github.com/latebit-io/nib/ai/llmconfig"
	"github.com/latebit-io/nib/ai/oauth"
)

// APIKeyEnv overrides nib's configured credentials, for headless
// deployments with no nib config on the box.
const APIKeyEnv = "ADAPTER_LLM_API_KEY"

// Provider resolves a provider from the API-key override, then nib's
// profile config, OAuth token store, and stored-key store. Returns nil
// when no credentials are available.
func Provider(logger *slog.Logger) llm.Provider {
	_, resolved := llmconfig.Resolve("")
	if key := os.Getenv(APIKeyEnv); key != "" {
		resolved.SetAPIKey(key)
	}

	for _, wire := range []func(*llmconfig.Resolved, *slog.Logger){wireOAuth, wireStoredKey} {
		if resolved.HasProvider() {
			break
		}
		wire(resolved, logger)
	}
	if !resolved.HasProvider() {
		return nil
	}
	logger.Info("llm provider", "profile", resolved.Profile, "model", resolved.DisplayModel())
	return resolved.NewProvider()
}

func wireOAuth(resolved *llmconfig.Resolved, logger *slog.Logger) {
	path, err := oauth.DefaultStorePath()
	if err != nil {
		logger.Debug("oauth store unavailable", "err", err)
		return
	}
	store, err := oauth.NewStore(path)
	if err != nil {
		logger.Warn("oauth store unreadable", "err", err)
		return
	}
	llmconfig.WireOAuth(resolved, store)
}

func wireStoredKey(resolved *llmconfig.Resolved, logger *slog.Logger) {
	path, err := oauth.DefaultKeyStorePath()
	if err != nil {
		logger.Debug("key store unavailable", "err", err)
		return
	}
	keyStore, err := oauth.NewKeyStore(path)
	if err != nil {
		logger.Warn("key store unreadable", "err", err)
		return
	}
	llmconfig.WireStoredKey(resolved, keyStore)
}
