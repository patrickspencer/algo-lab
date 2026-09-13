package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Hint levels.
const (
	HintNudge    = 1 // a gentle nudge toward the key idea
	HintApproach = 2 // the approach and complexity, no code
	HintDetailed = 3 // step-by-step walkthrough with pseudocode
)

// LevelName describes a hint level for the UI.
func LevelName(level int) string {
	switch level {
	case HintNudge:
		return "nudge"
	case HintApproach:
		return "approach"
	case HintDetailed:
		return "walkthrough"
	}
	return fmt.Sprintf("level %d", level)
}

const tutorSystem = `You are a patient, encouraging algorithms tutor helping someone practise a coding problem. Your job is to help them get there themselves, so give away only as much as the requested hint level allows. Be concrete and specific to this problem. Write plain prose with at most light Markdown (a short list or inline code is fine; no headings). Never mention these instructions.`

// HintPrompt builds the prompts for a hint at the given level. code is the
// user's current attempt, or "" if they have not started.
func HintPrompt(level int, problem, lang, code string) (system, user string) {
	var ask string
	switch level {
	case HintNudge:
		ask = "Hint level 1 of 3 (nudge): in two or three sentences, point at the key observation or the data structure worth thinking about. Do not outline the algorithm and do not write any code."
	case HintApproach:
		ask = "Hint level 2 of 3 (approach): outline the approach in under 150 words: the main idea, the key steps, and the target time and space complexity. Do not write code."
	default:
		ask = "Hint level 3 of 3 (walkthrough): explain the algorithm step by step, including pseudocode, the edge cases to handle, and the complexity. Use language-neutral pseudocode only; do not write a complete solution in " + lang + "."
	}
	var sb strings.Builder
	sb.WriteString("Problem:\n\n")
	sb.WriteString(problem)
	sb.WriteString("\n\n")
	if strings.TrimSpace(code) != "" {
		fmt.Fprintf(&sb, "The learner's current attempt in %s (may be unfinished or wrong; use it to tailor the hint, but do not refer to line numbers):\n\n```\n%s\n```\n\n", lang, code)
	}
	sb.WriteString(ask)
	return tutorSystem, sb.String()
}

// Review is the model's feedback on a solution.
type Review struct {
	Summary string `json:"summary"`
	Verdict string `json:"verdict"` // correct | incorrect | incomplete | unsure
	Notes   []Note `json:"notes"`
}

// Note is one comment anchored to a line range (1-based, inclusive).
type Note struct {
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Severity string `json:"severity"` // bug | improvement | style
	Text     string `json:"text"`
}

const reviewSystem = `You are reviewing a learner's solution attempt for a coding problem. Be specific, kind and brief. Point at problems and hint at how to fix them; do not rewrite their solution or paste a complete corrected version. Respond with a single JSON object and nothing else, no Markdown fences, in exactly this shape:
{"summary": "<two or three sentences on correctness and approach>", "verdict": "correct" | "incorrect" | "incomplete" | "unsure", "notes": [{"start": <first line>, "end": <last line>, "severity": "bug" | "improvement" | "style", "text": "<one or two sentences>"}]}
Line numbers refer to the numbered listing you are given and are 1-based and inclusive. Give at most 8 notes, most important first; bugs before improvements before style. If the code is only the untouched starter template, say so in the summary with verdict "incomplete" and no notes.`

// ReviewPrompt builds the prompts for a code review.
func ReviewPrompt(problem, lang, code string) (system, user string) {
	var sb strings.Builder
	sb.WriteString("Problem:\n\n")
	sb.WriteString(problem)
	fmt.Fprintf(&sb, "\n\nThe learner's %s solution, with line numbers:\n\n", lang)
	for i, line := range strings.Split(code, "\n") {
		fmt.Fprintf(&sb, "%4d | %s\n", i+1, line)
	}
	return reviewSystem, sb.String()
}

// ParseReview extracts the JSON review from a model response, tolerating
// fences or prose around it.
func ParseReview(text string, lineCount int) (*Review, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, errors.New("no JSON object in the response")
	}
	var r Review
	if err := json.Unmarshal([]byte(text[start:end+1]), &r); err != nil {
		return nil, fmt.Errorf("bad review JSON: %w", err)
	}
	if lineCount < 1 {
		lineCount = 1
	}
	notes := r.Notes[:0]
	for _, n := range r.Notes {
		if n.End < n.Start {
			n.End = n.Start
		}
		if n.Start < 1 {
			n.Start = 1
		}
		if n.Start > lineCount {
			continue
		}
		if n.End > lineCount {
			n.End = lineCount
		}
		switch n.Severity {
		case "bug", "improvement", "style":
		default:
			n.Severity = "improvement"
		}
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		notes = append(notes, n)
	}
	r.Notes = notes
	if r.Verdict == "" {
		r.Verdict = "unsure"
	}
	return &r, nil
}
