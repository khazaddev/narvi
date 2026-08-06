package rwx

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExecCLIRunner_CapturesStdoutStderrAndExitCode proves execCLIRunner's
// own plumbing (NOT RWX-specific logic — see doc.go for why no real `rwx`
// binary is available or needed here) correctly captures a real
// subprocess's stdout/stderr/exit code, using `sh`, a binary present on
// every CI runner this codebase already targets (unlike the pinned `rwx`
// CLI itself, which genuinely is not).
func TestExecCLIRunner_CapturesStdoutStderrAndExitCode(t *testing.T) {
	r := execCLIRunner{binary: "sh"}

	stdout, stderr, exitCode, err := r.Run(context.Background(),
		[]string{"-c", "echo out-line; echo err-line 1>&2; exit 3"},
		nil,
	)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (a clean, if nonzero, exit)", err)
	}
	if exitCode != 3 {
		t.Errorf("exitCode = %d, want 3", exitCode)
	}
	if got := strings.TrimSpace(string(stdout)); got != "out-line" {
		t.Errorf("stdout = %q, want %q", got, "out-line")
	}
	if got := strings.TrimSpace(string(stderr)); got != "err-line" {
		t.Errorf("stderr = %q, want %q", got, "err-line")
	}
}

func TestExecCLIRunner_ProcessNeverStarted(t *testing.T) {
	r := execCLIRunner{binary: "this-binary-does-not-exist-anywhere-narvi-rwx"}
	_, _, exitCode, err := r.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want a process-level error for a nonexistent binary")
	}
	if exitCode != -1 {
		t.Errorf("exitCode = %d, want -1 (process never completed)", exitCode)
	}
}

func TestExecCLIRunner_ContextDeadlineKillsProcess(t *testing.T) {
	r := execCLIRunner{binary: "sh"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, exitCode, err := r.Run(ctx, []string{"-c", "sleep 5"}, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want a process-level error for a killed process")
	}
	if exitCode != -1 {
		t.Errorf("exitCode = %d, want -1 (killed by context deadline)", exitCode)
	}
}

func TestExecCLIRunner_EnvIsExactlyWhatWasPassed(t *testing.T) {
	r := execCLIRunner{binary: "sh"}
	stdout, _, exitCode, err := r.Run(context.Background(),
		[]string{"-c", "echo $MY_TEST_VAR"},
		[]string{"MY_TEST_VAR=hello-rwx"},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if got := strings.TrimSpace(string(stdout)); got != "hello-rwx" {
		t.Errorf("stdout = %q, want %q", got, "hello-rwx")
	}
}
