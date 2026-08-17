package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

type distillProvider struct {
	events []llm.StreamEvent
	err    error
}

func (p distillProvider) Stream(context.Context, []llm.Message, []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan llm.StreamEvent, len(p.events))
	for _, event := range p.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func TestLLMDistiller(t *testing.T) {
	provider := distillProvider{events: []llm.StreamEvent{{Done: true, ToolCalls: []llm.ToolCall{{
		Function: llm.FunctionCall{Name: "distill_memory", Arguments: `{
			"title":" Parser\ntimezone fix ",
			"summary":"CI was flaky because tests assumed UTC; pinning TZ fixed it.",
			"tags":["CI","time\nzone","ci"],
			"importance":0.8
		}`},
	}}}}}
	d := NewLLMDistiller(provider, time.Second)
	got, err := d.Distill(context.Background(), []Message{{Role: "user", Content: "remember"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Parser timezone fix" || len(got.Tags) != 2 || got.Tags[0] != "ci" || got.Tags[1] != "time zone" {
		t.Fatalf("distillation = %+v", got)
	}
}

func TestLLMDistillerRejectsInvalidResponses(t *testing.T) {
	streamErr := errors.New("stream failed")
	for name, provider := range map[string]distillProvider{
		"provider":  {err: streamErr},
		"stream":    {events: []llm.StreamEvent{{Done: true, Err: streamErr}}},
		"truncated": {events: []llm.StreamEvent{{Done: true, Truncated: true}}},
		"no tool":   {events: []llm.StreamEvent{{Done: true}}},
		"bad json": {events: []llm.StreamEvent{{Done: true, ToolCalls: []llm.ToolCall{{
			Function: llm.FunctionCall{Name: "distill_memory", Arguments: `{`},
		}}}}},
		"bad fields": {events: []llm.StreamEvent{{Done: true, ToolCalls: []llm.ToolCall{{
			Function: llm.FunctionCall{Name: "distill_memory", Arguments: `{"title":"x","summary":"y","tags":["one"],"importance":2}`},
		}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewLLMDistiller(provider, time.Second).Distill(context.Background(), []Message{{Role: "user", Content: "x"}})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
