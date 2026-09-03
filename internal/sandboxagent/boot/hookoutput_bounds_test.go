package boot_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/sandboxboot"
	"github.com/narvidev/narvi/internal/sandboxagent/boot"
	"github.com/narvidev/narvi/internal/sandboxagent/supervisor"
)

// hookOutputTailMaxPendingBytes mirrors hookoutput.go's own unexported
// constant of the same name (64 KiB) -- hardcoded here rather than
// imported because it is a package-private implementation detail, exactly
// like TestRunHooks_NonFatalFailureCapturesOutputTail already hardcodes
// "120" to match hookOutputTailMaxLines. If the production bound ever
// changes, this test's expected counts must be updated alongside it.
const hookOutputTailMaxPendingBytes = 64 * 1024

// TestRunHooks_NewlineFreeOutputBoundedByBytes proves the actual defect
// Findings 1-4 describe: a hook that emits a large volume of output with
// NO newline (or '\r') anywhere in it -- e.g. a huge base64/minified blob,
// or a stalled progress renderer -- must not accumulate that output
// without limit in the pending "cur" buffer. It must instead be flushed,
// in hookOutputTailMaxPendingBytes-sized chunks, as synthetic truncation
// lines, with only the final undersized remainder left pending.
func TestRunHooks_NewlineFreeOutputBoundedByBytes(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()

	// Exactly two full hookOutputTailMaxPendingBytes chunks plus a 100-byte
	// remainder, all as ONE unbroken run of 'x' with no line boundary at
	// all -- chosen so the expected result is an exact, hand-computable
	// count: 2 synthetic truncation lines, then a genuine 100-byte pending
	// line (never itself hitting the cap).
	const remainder = 100
	total := 2*hookOutputTailMaxPendingBytes + remainder

	script := fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x\nexit 1\n", total)
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), script)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: false}} // secondary: non-fatal

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, nil, noopHookRerunTiming, 10*time.Second, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	rawTail, ok := handler.findAttr("output_tail")
	if !ok {
		t.Fatal("no log line carried an output_tail attribute")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}

	const wantLines = 3
	if len(lines) != wantLines {
		t.Fatalf("output_tail has %d lines, want exactly %d (got %v)", len(lines), wantLines, lines)
	}

	wantMarker := fmt.Sprintf("[[hookoutput: line truncated after %d bytes without a line break]]", hookOutputTailMaxPendingBytes)
	for i, want := range []string{wantMarker, wantMarker} {
		if lines[i] != want {
			t.Errorf("output_tail[%d] = %q, want synthetic truncation marker %q", i, lines[i], want)
		}
	}

	wantLast := strings.Repeat("x", remainder)
	if lines[2] != wantLast {
		t.Errorf("output_tail[2] = %q (%d bytes), want the exact %d-byte pending remainder", lines[2], len(lines[2]), remainder)
	}
}

// TestRunHooks_CarriageReturnProgressBecomesSuccessiveLines proves item 2:
// a '\r'-driven progress renderer (npm/yarn/cargo/docker-style) -- which
// emits no '\n' for the duration of a run -- is captured as successive
// SHORT lines (one per redraw), not as one ever-growing pending line. This
// is what keeps realistic progress-bar output from ever reaching the byte
// cap in the first place.
func TestRunHooks_CarriageReturnProgressBecomesSuccessiveLines(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()

	// 300 progress redraws, each terminated by '\r' (never '\n'), well over
	// the 120-line bound -- proving eviction still works per-'\r'-line.
	script := "i=0\nwhile [ \"$i\" -lt 300 ]; do printf 'progress %03d\\r' \"$i\"; i=$((i+1)); done\nexit 1\n"
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), script)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: false}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, nil, noopHookRerunTiming, 10*time.Second, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	rawTail, ok := handler.findAttr("output_tail")
	if !ok {
		t.Fatal("no log line carried an output_tail attribute")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}

	const wantLines = 120
	if len(lines) != wantLines {
		t.Fatalf("output_tail has %d lines, want exactly %d (got %v)", len(lines), wantLines, lines)
	}
	if got, want := lines[0], "progress 180"; got != want {
		t.Errorf("output_tail[0] = %q, want %q (300 redraws, oldest 180 evicted)", got, want)
	}
	if got, want := lines[len(lines)-1], "progress 299"; got != want {
		t.Errorf("output_tail[last] = %q, want %q", got, want)
	}
}

// TestRunHooks_CRLFNotDoubled proves the CRLF judgment call: a genuine
// Windows-style "\r\n" line ending must be treated as ONE line boundary,
// not two -- a hook emitting 3 CRLF-terminated lines must produce exactly
// 3 captured lines, never 6 (extra blank lines from the '\r' and '\n' each
// being read as their own separate boundary).
func TestRunHooks_CRLFNotDoubled(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()

	script := "printf 'crlf-line-0\\r\\n'\nprintf 'crlf-line-1\\r\\n'\nprintf 'crlf-line-2\\r\\n'\nexit 1\n"
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), script)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: false}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, nil, noopHookRerunTiming, 10*time.Second, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	rawTail, ok := handler.findAttr("output_tail")
	if !ok {
		t.Fatal("no log line carried an output_tail attribute")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}

	want := []string{"crlf-line-0", "crlf-line-1", "crlf-line-2"}
	if len(lines) != len(want) {
		t.Fatalf("output_tail = %v (%d lines), want exactly %v (%d lines, CRLF must not be doubled)", lines, len(lines), want, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("output_tail[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}
