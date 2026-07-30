package boot

import (
	"regexp"
	"strings"
	"sync"
)

// hookOutputTailMaxLines is "on the order of 120 lines" (§19.5(a)) -- the
// bound outputTail keeps for one hook run's own combined stdout+stderr.
const hookOutputTailMaxLines = 120

// ansiEscapePattern matches a CSI-style ANSI escape sequence (color codes,
// cursor movement, ...) -- e.g. "\x1b[31m", "\x1b[2K" -- so a hook's own
// colorized output (common for npm/yarn/cargo-style tooling) renders as
// plain, readable text in a boot log line rather than raw escape bytes.
var ansiEscapePattern = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// stripANSI removes every ANSI escape sequence ansiEscapePattern matches
// from s.
func stripANSI(s string) string {
	return ansiEscapePattern.ReplaceAllString(s, "")
}

// outputTail is a caller-held, bounded, line-oriented, ANSI-stripped
// ring buffer capturing the most recent hookOutputTailMaxLines lines of
// one hook run's own combined stdout+stderr (§19.5(a)). It implements
// io.Writer so it can be passed directly as BOTH supervisor.Spec.Stdout
// and Spec.Stderr (a single combined tail, mirroring how a terminal would
// interleave them) -- runHook constructs one PER hook run and holds it
// itself, independent of the bounded proc.Wait call: a timeout-triggered
// proc.Stop can therefore never lose a buffer that was never inside the
// cancelled operation to begin with (§19.5(a)'s own load-bearing
// requirement).
//
// Safe for concurrent Write calls (a spawned process's stdout/stderr are
// two independent OS pipes, read concurrently by the exec package) --
// guarded by a single mutex, matching supervisor.Process's own
// "spawned-process output is inherently concurrent" precedent.
type outputTail struct {
	mu    sync.Mutex
	lines []string
	cur   strings.Builder
}

// newOutputTail returns a ready, empty outputTail.
func newOutputTail() *outputTail {
	return &outputTail{}
}

// Write implements io.Writer: buffers p, splitting on newlines into
// complete lines (ANSI-stripped and appended, oldest evicted once the
// bound is exceeded) plus one trailing partial (not-yet-newline-
// terminated) line carried forward to the next Write call. Never returns
// an error -- exactly like a bytes.Buffer's own Write, which this type
// otherwise generalizes on (a bounded, line-splitting variant instead of
// an unbounded byte buffer).
func (t *outputTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cur.Write(p)
	for {
		s := t.cur.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		t.appendLineLocked(s[:idx])
		t.cur.Reset()
		t.cur.WriteString(s[idx+1:])
	}
	return len(p), nil
}

// appendLineLocked appends one already-newline-delimited line (ANSI
// stripped here, once, rather than per-Write-call) to t.lines, evicting
// the oldest entries once hookOutputTailMaxLines is exceeded. Caller must
// hold t.mu.
func (t *outputTail) appendLineLocked(line string) {
	t.lines = append(t.lines, stripANSI(line))
	if len(t.lines) > hookOutputTailMaxLines {
		t.lines = t.lines[len(t.lines)-hookOutputTailMaxLines:]
	}
}

// Lines returns a snapshot of the captured tail, oldest first, INCLUDING
// any partial (not-yet-newline-terminated) trailing line a hook's own
// final, unflushed write left behind (e.g. a script whose last line of
// output has no trailing newline) -- bounded to the same
// hookOutputTailMaxLines cap.
func (t *outputTail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	lines := make([]string, len(t.lines), len(t.lines)+1)
	copy(lines, t.lines)
	if t.cur.Len() > 0 {
		lines = append(lines, stripANSI(t.cur.String()))
		if len(lines) > hookOutputTailMaxLines {
			lines = lines[len(lines)-hookOutputTailMaxLines:]
		}
	}
	return lines
}
