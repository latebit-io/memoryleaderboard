package memory

import (
	"strings"
	"testing"
)

func int64ptr(value int64) *int64 { return &value }

func TestSourceTimes(t *testing.T) {
	start, end, ok := sourceTimes([]Message{
		{Timestamp: int64ptr(1704153600123)},
		{Timestamp: nil},
		{Timestamp: int64ptr(1704067200000)},
	})
	if !ok || start != "2024-01-01T00:00:00Z" || end != "2024-01-02T00:00:00.123Z" {
		t.Fatalf("sourceTimes = %q, %q, %v", start, end, ok)
	}
}

func TestFallbackDistillationAndRender(t *testing.T) {
	messages := []Message{{
		Role: "user", Content: "The parser CI fix pins TZ to UTC.", Timestamp: int64ptr(1704067200000),
	}}
	distilled := fallbackDistillation(messages, "s1", "r1")
	if distilled.Title != messages[0].Content || len(distilled.Tags) == 0 {
		t.Fatalf("fallback = %+v", distilled)
	}
	distilled.Summary = "CI expected UTC."
	body := renderMemory(distilled, messages)
	for _, want := range []string{"# The parser CI fix", "## Distilled memory", "CI expected UTC.", "## Raw messages", "2024-01-01T00:00:00Z", messages[0].Content} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q:\n%s", want, body)
		}
	}
}

func TestMessageHash(t *testing.T) {
	one, err := messageHash([]Message{{Role: "user", Content: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := messageHash([]Message{{Role: "user", Content: "one"}})
	two, _ := messageHash([]Message{{Role: "user", Content: "two"}})
	if one != again || one == two || !strings.HasPrefix(one, "sha256-") {
		t.Fatalf("hashes = %q, %q, %q", one, again, two)
	}
}

func TestRenderLegacyMemory(t *testing.T) {
	body := renderLegacyMemory("s1", "r1", []Message{{
		Role: "user", Content: "remember this", Timestamp: int64ptr(1704067200000),
	}})
	want := "# Session s1: r1\n\n- [1704067200000] **user**: remember this\n"
	if body != want {
		t.Fatalf("legacy body = %q, want %q", body, want)
	}
}
