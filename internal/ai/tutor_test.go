package ai

import (
	"strings"
	"testing"
)

func TestParseReview(t *testing.T) {
	text := "Sure, here it is:\n```json\n{\"summary\": \"Nearly there.\", \"verdict\": \"incorrect\", \"notes\": [" +
		"{\"start\": 4, \"end\": 3, \"severity\": \"bug\", \"text\": \"Off by one.\"}," +
		"{\"start\": 50, \"end\": 60, \"severity\": \"style\", \"text\": \"past the end\"}," +
		"{\"start\": 0, \"end\": 99, \"severity\": \"weird\", \"text\": \"clamped\"}," +
		"{\"start\": 2, \"end\": 2, \"severity\": \"bug\", \"text\": \"  \"}]}\n```\nHope that helps!"
	r, err := ParseReview(text, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary != "Nearly there." || r.Verdict != "incorrect" {
		t.Errorf("summary/verdict: %+v", r)
	}
	if len(r.Notes) != 2 {
		t.Fatalf("notes: %+v", r.Notes)
	}
	if r.Notes[0].Start != 4 || r.Notes[0].End != 4 {
		t.Errorf("end < start not fixed: %+v", r.Notes[0])
	}
	if r.Notes[1].Start != 1 || r.Notes[1].End != 10 || r.Notes[1].Severity != "improvement" {
		t.Errorf("clamping: %+v", r.Notes[1])
	}
	if _, err := ParseReview("no json here", 5); err == nil {
		t.Error("expected error for missing JSON")
	}
}

func TestPrompts(t *testing.T) {
	_, user := HintPrompt(HintNudge, "Two Sum…", "python3", "")
	if strings.Contains(user, "current attempt") {
		t.Error("empty code should not be included")
	}
	_, user = HintPrompt(HintDetailed, "Two Sum…", "golang", "func f(){}")
	if !strings.Contains(user, "current attempt") || !strings.Contains(user, "solution in golang") {
		t.Errorf("level 3 prompt: %s", user)
	}
	_, user = ReviewPrompt("P", "rust", "a\nb")
	if !strings.Contains(user, "   1 | a\n   2 | b\n") {
		t.Errorf("numbered listing: %q", user)
	}
}
