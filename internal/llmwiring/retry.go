package llmwiring

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

const (
	maxRateLimitRetries = 2
	maxRateLimitDelay   = 30 * time.Second
)

var retryAfterPattern = regexp.MustCompile(`(?i)try again in ([0-9]+(?:\.[0-9]+)?)(ms|s|m)`)

type rateLimitRetryProvider struct {
	llm.Provider
}

func withRateLimitRetries(provider llm.Provider) llm.Provider {
	return &rateLimitRetryProvider{Provider: provider}
}

func (p *rateLimitRetryProvider) Stream(ctx context.Context, messages []llm.Message, tools []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stream, err := p.Provider.Stream(ctx, messages, tools)
		if err == nil {
			return stream, nil
		}
		delay, retry := rateLimitDelay(err.Error())
		if !retry || attempt == maxRateLimitRetries {
			return nil, err
		}
		timer := time.NewTimer(delay + 100*time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		}
	}
}

func rateLimitDelay(message string) (time.Duration, bool) {
	if !strings.Contains(message, "status 429") || !strings.Contains(message, "rate_limit_exceeded") {
		return 0, false
	}
	match := retryAfterPattern.FindStringSubmatch(message)
	if len(match) != 3 {
		return 0, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	unit := time.Second
	switch strings.ToLower(match[2]) {
	case "ms":
		unit = time.Millisecond
	case "m":
		unit = time.Minute
	}
	delay := time.Duration(value * float64(unit))
	if delay < 0 || delay > maxRateLimitDelay {
		return 0, false
	}
	return delay, true
}
