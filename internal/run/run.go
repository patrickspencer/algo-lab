// Package run executes a snippet of code with a local interpreter or
// compiler and captures its output, for a scratchpad-style "hit run and see
// what happens" workflow.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Language describes how to run one language.
type Language struct {
	Slug  string     // language slug, e.g. "python3"
	Name  string     // display name
	File  string     // source file name inside the temp dir
	Steps [][]string // commands; {file} and {bin} are substituted
	Tool  string     // the executable that must be on PATH for this to work
}

// Result is what a run produced.
type Result struct {
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	ExitCode  int           `json:"exitCode"`
	Duration  time.Duration `json:"-"`
	Millis    int64         `json:"millis"`
	TimedOut  bool          `json:"timedOut"`
	Truncated bool          `json:"truncated"`
	Step      string        `json:"step,omitempty"` // "compile" when compilation failed
}

const (
	defaultTimeout = 10 * time.Second
	maxOutput      = 200 * 1024
)

var languages = []Language{
	{Slug: "python3", Name: "Python 3", File: "main.py", Tool: "python3", Steps: [][]string{{"python3", "-u", "{file}"}}},
	{Slug: "golang", Name: "Go", File: "main.go", Tool: "go", Steps: [][]string{{"go", "run", "{file}"}}},
	{Slug: "javascript", Name: "JavaScript (node)", File: "main.js", Tool: "node", Steps: [][]string{{"node", "{file}"}}},
	{Slug: "typescript", Name: "TypeScript (node --experimental-strip-types)", File: "main.ts", Tool: "node", Steps: [][]string{{"node", "--experimental-strip-types", "--no-warnings", "{file}"}}},
	{Slug: "ruby", Name: "Ruby", File: "main.rb", Tool: "ruby", Steps: [][]string{{"ruby", "{file}"}}},
	{Slug: "bash", Name: "Bash", File: "main.sh", Tool: "bash", Steps: [][]string{{"bash", "{file}"}}},
	{Slug: "c", Name: "C (gcc)", File: "main.c", Tool: "gcc", Steps: [][]string{{"gcc", "-O0", "-o", "{bin}", "{file}"}, {"{bin}"}}},
	{Slug: "cpp", Name: "C++ (g++)", File: "main.cpp", Tool: "g++", Steps: [][]string{{"g++", "-std=c++17", "-O0", "-o", "{bin}", "{file}"}, {"{bin}"}}},
	{Slug: "rust", Name: "Rust (rustc)", File: "main.rs", Tool: "rustc", Steps: [][]string{{"rustc", "-o", "{bin}", "{file}"}, {"{bin}"}}},
	{Slug: "java", Name: "Java (single-file)", File: "Main.java", Tool: "java", Steps: [][]string{{"java", "{file}"}}},
	{Slug: "swift", Name: "Swift", File: "main.swift", Tool: "swift", Steps: [][]string{{"swift", "{file}"}}},
	{Slug: "php", Name: "PHP", File: "main.php", Tool: "php", Steps: [][]string{{"php", "{file}"}}},
}

// aliases map alternate language slugs onto a runnable language.
var aliases = map[string]string{"python": "python3", "pythondata": "python3", "pandas": "python3"}

// Languages lists every language the runner knows, with availability.
func Languages() []LanguageInfo {
	out := make([]LanguageInfo, 0, len(languages))
	for _, l := range languages {
		_, err := exec.LookPath(l.Tool)
		out = append(out, LanguageInfo{Slug: l.Slug, Name: l.Name, Available: err == nil, Tool: l.Tool})
	}
	return out
}

// LanguageInfo is the public view of a language.
type LanguageInfo struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Tool      string `json:"tool"`
}

// Lookup returns the language for a slug (following aliases).
func Lookup(slug string) (Language, bool) {
	if a, ok := aliases[slug]; ok {
		slug = a
	}
	for _, l := range languages {
		if l.Slug == slug {
			return l, true
		}
	}
	return Language{}, false
}

// Available reports whether slug can be run on this machine.
func Available(slug string) bool {
	l, ok := Lookup(slug)
	if !ok {
		return false
	}
	_, err := exec.LookPath(l.Tool)
	return err == nil
}

// Timeout returns the per-run time limit ($ALGOLAB_RUN_TIMEOUT seconds).
func Timeout() time.Duration {
	if v := os.Getenv("ALGOLAB_RUN_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultTimeout
}

// Run writes code to a temp dir, runs each step, and returns the output of
// the last step (or of the step that failed). stdin is fed to the final step.
func Run(ctx context.Context, slug, code, stdin string) (Result, error) {
	lang, ok := Lookup(slug)
	if !ok {
		return Result{}, fmt.Errorf("run: no runner for %q", slug)
	}
	if _, err := exec.LookPath(lang.Tool); err != nil {
		return Result{}, fmt.Errorf("run: %s is not installed (needs %q on PATH)", lang.Name, lang.Tool)
	}
	dir, err := os.MkdirTemp("", "algo-lab-run-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	file := filepath.Join(dir, lang.File)
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		return Result{}, err
	}
	bin := filepath.Join(dir, "prog")
	sub := strings.NewReplacer("{file}", file, "{bin}", bin)

	ctx, cancel := context.WithTimeout(ctx, Timeout())
	defer cancel()
	start := time.Now()

	var res Result
	for i, step := range lang.Steps {
		last := i == len(lang.Steps)-1
		args := make([]string, len(step))
		for j, a := range step {
			args[j] = sub.Replace(a)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "GOFLAGS=-mod=mod", "HOME="+os.Getenv("HOME"))
		if last {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var stdout, stderr limitedBuffer
		stdout.limit, stderr.limit = maxOutput, maxOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()

		res = Result{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.truncated || stderr.truncated}
		res.Duration = time.Since(start)
		res.Millis = res.Duration.Milliseconds()
		if ctx.Err() == context.DeadlineExceeded {
			res.TimedOut = true
			res.ExitCode = -1
			res.Stderr += fmt.Sprintf("\n[killed after %s]", Timeout())
			return res, nil
		}
		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
			res.ExitCode = 0
		case errors.As(runErr, &exitErr):
			res.ExitCode = exitErr.ExitCode()
		default:
			return res, fmt.Errorf("run: %w", runErr)
		}
		if res.ExitCode != 0 {
			if !last {
				res.Step = "compile"
			}
			return res, nil
		}
	}
	return res, nil
}

// limitedBuffer keeps at most limit bytes and remembers if it dropped any.
// It deliberately does not embed bytes.Buffer: io.Copy would then use the
// promoted ReadFrom and bypass the cap.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.truncated = true
		b.buf.Write(p[:room])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	s := b.buf.String()
	if b.truncated {
		s += "\n[output truncated]"
	}
	return s
}
