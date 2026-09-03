package boot_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/sandboxboot"
	"github.com/narvidev/narvi/internal/sandboxagent/boot"
	"github.com/narvidev/narvi/internal/sandboxagent/supervisor"
)

// recordingHandler is a minimal slog.Handler capturing every record's own
// attributes, so a test can inspect exactly what a Warn call logged (here:
// the "output_tail" attribute runRepoHooks attaches to a non-fatal hook
// failure, §19.5(a)) without depending on slog's own text/JSON rendering.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

// findAttr scans every captured record, MOST RECENT FIRST, for one
// carrying attrName, returning the first match. Unlike inspecting only the
// single most recent record (which breaks the instant any later,
// unrelated log line -- e.g. a "boot complete" Info line -- is added
// after the property under test was already recorded), this holds as long
// as SOME record carries the attribute, regardless of what else got
// logged afterward.
func (h *recordingHandler) findAttr(attrName string) (slog.Value, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		var found slog.Value
		var ok bool
		h.records[i].Attrs(func(a slog.Attr) bool {
			if a.Key == attrName {
				found = a.Value
				ok = true
				return false
			}
			return true
		})
		if ok {
			return found, true
		}
	}
	return slog.Value{}, false
}

// TestRunHooks_NonFatalFailureCapturesOutputTail proves §19.5(a) end to
// end: a real script producing MORE than the 120-line bound, using ANSI
// color codes, that ultimately fails non-fatally (a secondary repo's
// start.sh) -- the resulting Warn log line's own "output_tail" attribute
// is bounded to at most 120 lines and contains no raw ANSI escape bytes.
func TestRunHooks_NonFatalFailureCapturesOutputTail(t *testing.T) {
	// Not t.Parallel(): swaps slog's global default logger for the
	// duration of this test to capture what runRepoHooks actually logged,
	// mirroring TestRunHooks_EnvExcludesSessionConfig's own precedent of
	// avoiding t.Parallel() whenever a test needs exclusive access to
	// shared global state.
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()

	// A script producing 200 lines (well over the 120-line bound), each
	// prefixed with a real ANSI color escape sequence, then failing.
	var script strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&script, "printf '\\033[31mline-%d\\033[0m\\n'\n", i)
	}
	script.WriteString("exit 1\n")
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), script.String())

	sup := supervisor.New()
	// A SECONDARY repo (§3.4/§6.4: secondary start.sh failure is only ever
	// a warning) -- this is the non-fatal case §19.5(a) exists to fix.
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

	for _, line := range lines {
		if strings.ContainsRune(line, '\x1b') {
			t.Errorf("output_tail line %q still contains a raw ANSI escape byte, want it stripped", line)
		}
	}

	// EXACT count and content, not just "under some max": the script wrote
	// 200 newline-terminated lines and nothing else, so the 120-line bound
	// must evict precisely the first 80 (keeping line-80..line-199) -- an
	// assertion that only checked len(lines) <= 120 would stay green even
	// if eviction over-truncated to, say, the last 5 lines.
	const wantLines = 120
	if len(lines) != wantLines {
		t.Fatalf("output_tail has %d lines, want exactly %d", len(lines), wantLines)
	}
	if got, want := lines[0], "line-80"; got != want {
		t.Errorf("output_tail[0] = %q, want %q (oldest surviving line after evicting the first 80)", got, want)
	}
	if got, want := lines[len(lines)-1], "line-199"; got != want {
		t.Errorf("output_tail[last] = %q, want %q (a real tail, not a head)", got, want)
	}
}

// TestRunHooks_FatalFailureAlsoLogsStructuredOutputTail proves the fatal
// path attaches the SAME structured "output_tail" attribute the non-fatal
// path does (item 4), rather than only interpolating tail.Lines() into the
// returned error's own message with %v -- so an operator can grep for
// output_tail uniformly regardless of which outcome a hook took.
func TestRunHooks_FatalFailureAlsoLogsStructuredOutputTail(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()

	// build mode: setup.sh failure is always fatal, regardless of primary
	// (mirrors TestRunHooks_FatalFailureStopsImmediately's own precedent).
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"),
		"echo 'distinctive fatal setup failure' >&2\nexit 1")

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild, nil,
		nil, nil, noopHookRerunTiming, 10*time.Second, time.Second, time.Millisecond)
	if err == nil {
		t.Fatal("RunHooks() error = nil, want an error (build mode's setup.sh failure is fatal)")
	}

	rawTail, ok := handler.findAttr("output_tail")
	if !ok {
		t.Fatal("no log line carried an output_tail attribute for the FATAL failure -- item 4 requires the same structured attribute on both paths")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}

	found := false
	for _, line := range lines {
		if line == "distinctive fatal setup failure" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("output_tail = %v, want it to contain the setup.sh failure's own diagnostic output", lines)
	}
}

// TestRecordingHandlerFindAttr_SurvivesLaterUnrelatedLogLine guards the
// exact fragility Finding 5 identified: a lookup that only inspects the
// single most recent slog.Record breaks the instant any later, unrelated
// record (carrying no output_tail attribute at all) is logged after the
// property under test. findAttr must keep finding the earlier record.
func TestRecordingHandlerFindAttr_SurvivesLaterUnrelatedLogLine(t *testing.T) {
	handler := &recordingHandler{}
	logger := slog.New(handler)

	logger.Warn("boot: hook failed, continuing", "output_tail", []string{"captured line"})
	// An unrelated later record with no output_tail attribute at all --
	// e.g. a "boot complete" Info line added downstream of the hook loop.
	logger.Info("boot: unrelated later line")

	rawTail, ok := handler.findAttr("output_tail")
	if !ok {
		t.Fatal("findAttr() found nothing, want the earlier record's output_tail attribute")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok || len(lines) != 1 || lines[0] != "captured line" {
		t.Errorf("findAttr(\"output_tail\") = %v, want [\"captured line\"]", rawTail.Any())
	}
}
