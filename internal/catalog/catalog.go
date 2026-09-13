// Package catalog holds the data types that describe a coding problem and its
// starter code. It is deliberately self-contained: the app builds its problem
// set from the embedded dataset and never talks to any external service.
package catalog

import "strings"

// TopicTag is a problem topic such as "Array" or "Hash Table".
type TopicTag struct {
	Name string `json:"name"`
}

// Problem is one row in the problem list.
type Problem struct {
	Status     string     `json:"status"` // "ac" (solved), "notac" (attempted) or ""
	FrontendID string     `json:"frontendQuestionId"`
	Title      string     `json:"title"`
	TitleSlug  string     `json:"titleSlug"`
	Difficulty string     `json:"difficulty"`
	AcRate     float64    `json:"acRate"`
	PaidOnly   bool       `json:"paidOnly"`
	TopicTags  []TopicTag `json:"topicTags"`
}

// CodeSnippet is the starter code for one language.
type CodeSnippet struct {
	Lang     string `json:"lang"`     // display name, e.g. "Python3"
	LangSlug string `json:"langSlug"` // e.g. "python3"
	Code     string `json:"code"`
}

// Detail is the full description of a single problem.
type Detail struct {
	QuestionID       string        `json:"questionId"`
	FrontendID       string        `json:"questionFrontendId"`
	Title            string        `json:"title"`
	TitleSlug        string        `json:"titleSlug"`
	Difficulty       string        `json:"difficulty"`
	Content          string        `json:"content"` // HTML
	ExampleTestcases string        `json:"exampleTestcases"`
	ExampleCases     []string      `json:"exampleTestcaseList"`
	MetaData         string        `json:"metaData"`
	Hints            []string      `json:"hints"`
	TopicTags        []TopicTag    `json:"topicTags"`
	Likes            int           `json:"likes"`
	Dislikes         int           `json:"dislikes"`
	PaidOnly         bool          `json:"isPaidOnly"`
	CodeSnippets     []CodeSnippet `json:"codeSnippets"`
}

// HasContent reports whether the cached description actually has a body.
func (d *Detail) HasContent() bool { return len(strings.TrimSpace(d.Content)) > 0 }

// Snippet returns the starter code for a language slug ("" if none).
func (d *Detail) Snippet(langSlug string) string {
	for _, s := range d.CodeSnippets {
		if s.LangSlug == langSlug {
			return s.Code
		}
	}
	return ""
}
