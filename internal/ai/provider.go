// Package ai asks an LLM for hints and code reviews. It can talk to the
// Anthropic API directly, or shell out to the claude or codex CLIs so that an
// existing subscription can be used without an API key.
package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Provider produces a completion for a system + user prompt.
type Provider interface {
	Name() string
	Complete(ctx context.Context, system, user string) (string, error)
}

// ErrUnavailable is returned by Detect when no backend can be found.
var ErrUnavailable = errors.New("no AI backend available: set ANTHROPIC_API_KEY, install the claude or codex CLI, or set ALGOLAB_AI")

const (
	defaultModel = "claude-opus-5"
	cliTimeout   = 3 * time.Minute
)

// aiModel is the model override for whichever backend is active
// (ALGOLAB_AI_MODEL). For the claude/codex CLIs it is passed to their
// --model flag (aliases like "haiku"/"sonnet" work); for the Anthropic API it
// is the model id. Empty means the backend's own default.
func aiModel() string { return strings.TrimSpace(os.Getenv("ALGOLAB_AI_MODEL")) }

// Detect picks a backend. $ALGOLAB_AI forces one of "anthropic",
// "claude", "codex" or "command" (a shell command from $ALGOLAB_AI_COMMAND
// that reads the prompt on stdin and prints the answer); otherwise the first
// available of: Anthropic API key, claude CLI, codex CLI.
func Detect() (Provider, error) {
	switch strings.ToLower(os.Getenv("ALGOLAB_AI")) {
	case "command":
		c := os.Getenv("ALGOLAB_AI_COMMAND")
		if c == "" {
			return nil, fmt.Errorf("ALGOLAB_AI=command needs ALGOLAB_AI_COMMAND")
		}
		return commandCLI{command: c}, nil
	case "anthropic", "api":
		return newAnthropic(), nil
	case "claude":
		if _, err := exec.LookPath("claude"); err != nil {
			return nil, fmt.Errorf("ALGOLAB_AI=claude but the claude CLI is not on PATH")
		}
		return claudeCLI{}, nil
	case "codex":
		if _, err := exec.LookPath("codex"); err != nil {
			return nil, fmt.Errorf("ALGOLAB_AI=codex but the codex CLI is not on PATH")
		}
		return codexCLI{}, nil
	case "", "auto":
	default:
		return nil, fmt.Errorf("unknown ALGOLAB_AI=%q (use anthropic, claude, codex or command)", os.Getenv("ALGOLAB_AI"))
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return newAnthropic(), nil
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return claudeCLI{}, nil
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return codexCLI{}, nil
	}
	// The Anthropic SDK also reads OAuth profiles written by `ant auth login`.
	if dir, err := os.UserConfigDir(); err == nil {
		if _, err := os.Stat(filepath.Join(dir, "anthropic")); err == nil {
			return newAnthropic(), nil
		}
	}
	return nil, ErrUnavailable
}

// ---------------------------------------------------------------------------
// Anthropic API

type anthropicAPI struct {
	client anthropic.Client
	model  string
}

func newAnthropic() Provider {
	model := os.Getenv("ALGOLAB_AI_MODEL")
	if model == "" {
		model = defaultModel
	}
	return &anthropicAPI{client: anthropic.NewClient(), model: model}
}

func (a *anthropicAPI) Name() string { return "anthropic/" + a.model }

func (a *anthropicAPI) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := a.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 4096,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user))},
		// Server-side refusal fallbacks: if a safety classifier declines the
		// request, Anthropic re-serves it with a fallback model in the same call.
		Betas:     []anthropic.AnthropicBeta{"server-side-fallback-2026-07-01"},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
	})
	if err != nil {
		return "", err
	}
	if string(resp.StopReason) == "refusal" {
		return "", fmt.Errorf("the model declined this request (%s)", resp.StopDetails.Category)
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// ---------------------------------------------------------------------------
// claude CLI (uses the user's Claude Code login)

type claudeCLI struct{}

func (claudeCLI) Name() string {
	if m := aiModel(); m != "" {
		return "claude/" + m
	}
	return "claude"
}

func (claudeCLI) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	args := []string{"-p", "--output-format", "text", "--system-prompt", system, "--tools", "", "--no-session-persistence"}
	if m := aiModel(); m != "" {
		args = append(args, "--model", m)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(user)
	// A nested Claude Code session refuses to start; this is a plain query.
	cmd.Env = withoutEnv(os.Environ(), "CLAUDECODE")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("AI request failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ---------------------------------------------------------------------------
// codex CLI (uses the user's OpenAI login)

type codexCLI struct{}

func (codexCLI) Name() string {
	if m := aiModel(); m != "" {
		return "codex/" + m
	}
	return "codex"
}

func (codexCLI) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "algo-lab-codex-*.txt")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// codex has no separate system prompt in exec mode; fold it into the prompt.
	prompt := system + "\n\n---\n\n" + user
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "--color", "never"}
	if m := aiModel(); m != "" {
		args = append(args, "-m", m)
	}
	args = append(args, "-o", tmp.Name(), "-")
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = nil, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("AI request failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// ---------------------------------------------------------------------------
// Arbitrary command (local models, other CLIs)

type commandCLI struct{ command string }

func (c commandCLI) Name() string { return "command" }

func (c commandCLI) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.command) //nolint:gosec
	cmd.Stdin = strings.NewReader(system + "\n\n---\n\n" + user)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("AI request failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}
