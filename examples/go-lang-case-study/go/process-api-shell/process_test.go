package processapishell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCapturesStdoutAndExitCode(t *testing.T) {
	printfPath := requireCommand(t, "printf")

	result, err := Run(printfPath, "hello from child\n")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello from child\n" {
		t.Fatalf("stdout = %q, want %q", result.Stdout, "hello from child\n")
	}
}

func TestRunReportsNonZeroExitCode(t *testing.T) {
	shPath := requireCommand(t, "sh")

	result, err := Run(shPath, "-c", "exit 7")
	if err == nil {
		t.Fatal("Run returned nil error for non-zero exit")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error type = %T, want *exec.ExitError", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
}

func TestRunRedirectWritesStdoutToFile(t *testing.T) {
	printfPath := requireCommand(t, "printf")
	outPath := filepath.Join(t.TempDir(), "out.txt")

	result, err := RunRedirect(printfPath, []string{"line one\nline two\n"}, outPath)
	if err != nil {
		t.Fatalf("RunRedirect returned error: %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "" {
		t.Fatalf("captured stdout = %q, want empty because stdout was redirected", result.Stdout)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read redirected output: %v", err)
	}
	if string(got) != "line one\nline two\n" {
		t.Fatalf("redirected output = %q", string(got))
	}
}

func TestRunPipelineConnectsTwoCommands(t *testing.T) {
	grepPath := requireCommand(t, "grep")
	wcPath := requireCommand(t, "wc")

	logPath := filepath.Join(t.TempDir(), "app.log")
	logContent := strings.Join([]string{
		"info booted",
		"error could not connect",
		"warning retrying",
		"error timeout",
		"info stopped",
	}, "\n") + "\n"

	if err := os.WriteFile(logPath, []byte(logContent), 0o600); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}

	result, err := RunPipeline(
		Command{Name: grepPath, Args: []string{"error", logPath}},
		Command{Name: wcPath, Args: []string{"-l"}},
	)
	if err != nil {
		t.Fatalf("RunPipeline returned error: %v\nstderr: %s", err, result.Stderr)
	}

	if result.Left.ExitCode != 0 {
		t.Fatalf("left exit code = %d, want 0", result.Left.ExitCode)
	}
	if result.Right.ExitCode != 0 {
		t.Fatalf("right exit code = %d, want 0", result.Right.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "2" {
		t.Fatalf("pipeline stdout = %q, want count 2", result.Stdout)
	}
}

func requireCommand(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%q not available on PATH", name)
	}
	return path
}
