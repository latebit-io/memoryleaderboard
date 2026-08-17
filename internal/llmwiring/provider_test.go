package llmwiring

import (
	"io"
	"log/slog"
	"testing"

	"github.com/latebit-io/nib/ai/oauth"
)

func TestExplicitEndpointRequiresExplicitKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LLM_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("LLM_MODEL", "gpt-4o-mini")
	for _, key := range []string{
		APIKeyEnv, "LLM_API_KEY", "ANTHROPIC_API_KEY", "SAKANA_API_KEY",
		"GEMINI_API_KEY", "MINIMAX_API_KEY", "OPENROUTER_API_KEY",
	} {
		t.Setenv(key, "")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyPath, err := oauth.DefaultKeyStorePath()
	if err != nil {
		t.Fatal(err)
	}
	keyStore, err := oauth.NewKeyStore(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyStore.Put("env", "stored-key"); err != nil {
		t.Fatal(err)
	}

	if provider := Provider(logger); provider != nil {
		t.Fatal("Provider reused ambient credentials for explicit endpoint")
	}
	t.Setenv(APIKeyEnv, "test-key")
	if provider := Provider(logger); provider == nil {
		t.Fatal("Provider rejected explicit endpoint key")
	}
}
