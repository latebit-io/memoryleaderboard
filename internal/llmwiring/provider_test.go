package llmwiring

import (
	"io"
	"log/slog"
	"testing"
)

func TestExplicitEndpointRequiresExplicitKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LLM_BASE_URL", "https://api.minimax.io/v1")
	t.Setenv("LLM_MODEL", "MiniMax-M3")
	for _, key := range []string{
		APIKeyEnv, "LLM_API_KEY", "ANTHROPIC_API_KEY", "SAKANA_API_KEY",
		"GEMINI_API_KEY", "MINIMAX_API_KEY", "OPENROUTER_API_KEY",
	} {
		t.Setenv(key, "")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if provider := Provider(logger); provider != nil {
		t.Fatal("Provider reused ambient credentials for explicit endpoint")
	}
	t.Setenv(APIKeyEnv, "test-key")
	if provider := Provider(logger); provider == nil {
		t.Fatal("Provider rejected explicit endpoint key")
	}
}
