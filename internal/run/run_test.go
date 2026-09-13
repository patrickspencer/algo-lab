package run

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func needPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
}

func TestRunPython(t *testing.T) {
	needPython(t)
	res, err := Run(context.Background(), "python3", "import sys\nprint('hello', 1+1)\nprint('err', file=sys.stderr)\nsys.exit(3)", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hello 2\n" || res.Stderr != "err\n" || res.ExitCode != 3 || res.TimedOut {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunStdinAndAlias(t *testing.T) {
	needPython(t)
	res, err := Run(context.Background(), "python", "print(input()[::-1])", "abc\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "cba\n" || res.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	needPython(t)
	t.Setenv("ALGOLAB_RUN_TIMEOUT", "1")
	start := time.Now()
	res, err := Run(context.Background(), "python3", "while True: pass", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || time.Since(start) > 5*time.Second || !strings.Contains(res.Stderr, "killed") {
		t.Fatalf("expected timeout: %+v", res)
	}
}

func TestRunTruncates(t *testing.T) {
	needPython(t)
	res, err := Run(context.Background(), "python3", "print('x' * 500000)", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) > maxOutput+40 {
		t.Fatalf("expected truncation: truncated=%v len=%d", res.Truncated, len(res.Stdout))
	}
}

func TestUnknownLanguage(t *testing.T) {
	if _, err := Run(context.Background(), "brainfuck", "", ""); err == nil {
		t.Fatal("expected error")
	}
	if Available("brainfuck") {
		t.Fatal("brainfuck should not be available")
	}
	if len(Languages()) == 0 {
		t.Fatal("no languages")
	}
}
