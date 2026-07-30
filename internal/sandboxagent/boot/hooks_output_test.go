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

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
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

// attr looks up attrName on the LAST captured record, returning its value
// and whether it was found at all.
func (h *recordingHandler) lastAttr(attrName string) (slog.Value, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		return slog.Value{}, false
	}
	r := h.records[len(h.records)-1]
	var found slog.Value
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == attrName {
			found = a.Value
			ok = true
			return false
		}
		return true
	})
	return found, ok
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
		10*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	rawTail, ok := handler.lastAttr("output_tail")
	if !ok {
		t.Fatal("no Warn log line carried an output_tail attribute")
	}

	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}

	if len(lines) > 120 {
		t.Errorf("output_tail has %d lines, want at most 120", len(lines))
	}
	if len(lines) == 0 {
		t.Fatal("output_tail is empty, want the captured script output")
	}

	for _, line := range lines {
		if strings.ContainsRune(line, '\x1b') {
			t.Errorf("output_tail line %q still contains a raw ANSI escape byte, want it stripped", line)
		}
	}

	// The tail is a TAIL: the last captured line should be near the end of
	// the script's own 200 lines (line-199 or line-198, depending on
	// whether the trailing "exit 1" line ever produced its own tail
	// entry), not the very first ones -- proving truncation kept the most
	// RECENT output, not the oldest.
	last := lines[len(lines)-1]
	if !strings.Contains(last, "line-19") {
		t.Errorf("last captured output_tail line = %q, want it to be near the end of the script's own output (a real tail, not a head)", last)
	}
}
