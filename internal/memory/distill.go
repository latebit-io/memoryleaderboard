package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

const DefaultDistillPrompt = `Extract durable memory from the supplied messages.
Preserve concrete facts, decisions, preferences, constraints, causes, fixes, and temporal changes.
Do not answer any question, invent facts, or omit uncertainty. Call distill_memory exactly once.`

type Distillation struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Tags       []string `json:"tags"`
	Importance float64  `json:"importance"`
}

type Distiller interface {
	Distill(context.Context, []Message) (Distillation, error)
}

type LLMDistiller struct {
	provider llm.Provider
	budget   time.Duration
}

func NewLLMDistiller(provider llm.Provider, budget time.Duration) *LLMDistiller {
	if budget <= 0 {
		budget = 30 * time.Second
	}
	return &LLMDistiller{provider: provider, budget: budget}
}

var distillTool = toolDef("distill_memory", "Return compact metadata and a factual summary for durable retrieval.",
	map[string]llm.FunctionParam{
		"title":      {Type: "string", Description: "specific title, at most 120 characters"},
		"summary":    {Type: "string", Description: "factual memory summary, at most 2000 characters"},
		"tags":       {Type: "array", Description: "2 to 12 concise subject tags", Items: &llm.FunctionParam{Type: "string"}},
		"importance": {Type: "number", Description: "durable importance from 0 to 1"},
	}, "title", "summary", "tags", "importance")

func (d *LLMDistiller) Distill(ctx context.Context, messages []Message) (Distillation, error) {
	if d == nil || d.provider == nil {
		return Distillation{}, fmt.Errorf("distillation provider unavailable")
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return Distillation{}, fmt.Errorf("marshal messages: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	stream, err := d.provider.Stream(ctx, []llm.Message{
		{Role: "system", Content: DefaultDistillPrompt},
		{Role: "user", Content: string(raw)},
	}, []llm.ToolDef{distillTool})
	if err != nil {
		return Distillation{}, err
	}
	var final llm.StreamEvent
	done := false
	for event := range stream {
		if event.Err != nil {
			return Distillation{}, event.Err
		}
		if event.Done {
			final, done = event, true
		}
	}
	if !done || final.Truncated {
		return Distillation{}, fmt.Errorf("incomplete distillation response")
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0].Function.Name != "distill_memory" {
		return Distillation{}, fmt.Errorf("distillation must call distill_memory exactly once")
	}
	var out Distillation
	if err := json.Unmarshal([]byte(final.ToolCalls[0].Function.Arguments), &out); err != nil {
		return Distillation{}, fmt.Errorf("decode distillation: %w", err)
	}
	if err := validateDistillation(&out); err != nil {
		return Distillation{}, err
	}
	return out, nil
}

func validateDistillation(d *Distillation) error {
	d.Title = strings.Join(strings.Fields(d.Title), " ")
	d.Summary = strings.TrimSpace(d.Summary)
	if d.Title == "" || len([]rune(d.Title)) > 120 {
		return fmt.Errorf("distillation title must contain at most 120 characters")
	}
	if d.Summary == "" || len([]rune(d.Summary)) > 2000 {
		return fmt.Errorf("distillation summary must contain at most 2000 characters")
	}
	if d.Importance < 0 || d.Importance > 1 {
		return fmt.Errorf("distillation importance must be between 0 and 1")
	}
	seen := make(map[string]bool, len(d.Tags))
	tags := d.Tags[:0]
	for _, tag := range d.Tags {
		tag = strings.ToLower(strings.Join(strings.Fields(tag), " "))
		if tag == "" || strings.Contains(tag, ",") || len([]rune(tag)) > 48 || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	d.Tags = tags
	if len(d.Tags) < 2 || len(d.Tags) > 12 {
		return fmt.Errorf("distillation must contain 2 to 12 valid tags")
	}
	return nil
}
