package llmwiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

type retryTestProvider struct {
	calls int
	err   error
}

func (p *retryTestProvider) Stream(context.Context, []llm.Message, []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	p.calls++
	if p.calls == 1 {
		return nil, p.err
	}
	stream := make(chan llm.StreamEvent)
	close(stream)
	return stream, nil
}

func TestRateLimitRetryProvider(t *testing.T) {
	provider := &retryTestProvider{err: errors.New("api error: status 429: rate_limit_exceeded; Please try again in 0ms")}
	if _, err := withRateLimitRetries(provider).Stream(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls = %d, want 2", provider.calls)
	}
}

func TestRateLimitDelay(t *testing.T) {
	for _, test := range []struct {
		message string
		want    time.Duration
		ok      bool
	}{
		{"api error: status 429: rate_limit_exceeded; Please try again in 8.64s", 8640 * time.Millisecond, true},
		{"api error: status 429: rate_limit_exceeded; Please try again in 250ms", 250 * time.Millisecond, true},
		{"api error: status 429: insufficient_quota; Please try again in 1s", 0, false},
		{"api error: status 429: rate_limit_exceeded; Please try again in 2m", 0, false},
	} {
		got, ok := rateLimitDelay(test.message)
		if got != test.want || ok != test.ok {
			t.Errorf("rateLimitDelay(%q) = %v, %t; want %v, %t", test.message, got, ok, test.want, test.ok)
		}
	}
}
