// Package dataset holds an embedded set of original, public-domain interview
// problems (statements written from scratch, not copied from any site). They
// can be served in place of third-party content so a problem set is fully
// redistributable.
package dataset

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed problems.json
var raw []byte

// Param is one function argument.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Example is a worked example.
type Example struct {
	Input       string `json:"input"`
	Output      string `json:"output"`
	Explanation string `json:"explanation"`
}

// Problem is one original problem.
type Problem struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	Difficulty   string    `json:"difficulty"`
	Tags         []string  `json:"tags"`
	Statement    string    `json:"statement"` // Markdown
	FunctionName string    `json:"functionName"`
	Params       []Param   `json:"params"`
	Returns      string    `json:"returns"`
	Examples     []Example `json:"examples"`
	Constraints  []string  `json:"constraints"`
}

var (
	all    []Problem
	bySlug = map[string]Problem{}
)

func init() {
	_ = json.Unmarshal(raw, &all)
	for _, p := range all {
		bySlug[p.Slug] = p
	}
}

// All returns every embedded problem in order.
func All() []Problem { return all }

// Slugs returns the problem slugs in order.
func Slugs() []string {
	out := make([]string, 0, len(all))
	for _, p := range all {
		out = append(out, p.Slug)
	}
	return out
}

// BySlug returns the problem for a slug, if present.
func BySlug(slug string) (Problem, bool) { p, ok := bySlug[slug]; return p, ok }

// Count is how many problems are embedded.
func Count() int { return len(all) }

// StarterCode returns a best-effort function stub for a language slug.
func (p Problem) StarterCode(lang string) string {
	fn := p.FunctionName
	if fn == "" {
		fn = "solve"
	}
	switch lang {
	case "python", "python3":
		names := make([]string, 0, len(p.Params))
		for _, a := range p.Params {
			names = append(names, a.Name)
		}
		return fmt.Sprintf("class Solution:\n    def %s(self%s):\n        \n", fn, joinPrefix(names, ", "))
	case "javascript", "typescript":
		names := make([]string, 0, len(p.Params))
		for _, a := range p.Params {
			names = append(names, a.Name)
		}
		return fmt.Sprintf("function %s(%s) {\n    \n}\n", fn, strings.Join(names, ", "))
	case "golang":
		args := make([]string, 0, len(p.Params))
		for _, a := range p.Params {
			args = append(args, a.Name+" "+goType(a.Type))
		}
		ret := goType(p.Returns)
		if ret != "" {
			ret = " " + ret
		}
		return fmt.Sprintf("func %s(%s)%s {\n    \n}\n", fn, strings.Join(args, ", "), ret)
	case "java":
		args := make([]string, 0, len(p.Params))
		for _, a := range p.Params {
			args = append(args, javaType(a.Type)+" "+a.Name)
		}
		ret := javaType(p.Returns)
		if ret == "" {
			ret = "void"
		}
		return fmt.Sprintf("class Solution {\n    public %s %s(%s) {\n        \n    }\n}\n", ret, fn, strings.Join(args, ", "))
	case "cpp":
		args := make([]string, 0, len(p.Params))
		for _, a := range p.Params {
			args = append(args, cppType(a.Type)+" "+a.Name)
		}
		ret := cppType(p.Returns)
		if ret == "" {
			ret = "void"
		}
		return fmt.Sprintf("class Solution {\npublic:\n    %s %s(%s) {\n        \n    }\n};\n", ret, fn, strings.Join(args, ", "))
	}
	return ""
}

func joinPrefix(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	return sep + strings.Join(items, sep)
}

func mapType(t string, m map[string]string) string {
	t = strings.TrimSpace(t)
	if v, ok := m[t]; ok {
		return v
	}
	return t // best effort: leave unknown types as-is
}
func goType(t string) string {
	return mapType(t, map[string]string{"int": "int", "int[]": "[]int", "int[][]": "[][]int", "string": "string", "string[]": "[]string", "bool": "bool", "boolean": "bool", "double": "float64", "float": "float64", "char": "byte", "": ""})
}
func javaType(t string) string {
	return mapType(t, map[string]string{"int": "int", "int[]": "int[]", "int[][]": "int[][]", "string": "String", "string[]": "String[]", "bool": "boolean", "boolean": "boolean", "double": "double", "float": "double", "char": "char", "": ""})
}
func cppType(t string) string {
	return mapType(t, map[string]string{"int": "int", "int[]": "vector<int>", "int[][]": "vector<vector<int>>", "string": "string", "string[]": "vector<string>", "bool": "bool", "boolean": "bool", "double": "double", "float": "double", "char": "char", "": ""})
}
