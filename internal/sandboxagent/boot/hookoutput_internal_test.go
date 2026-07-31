package boot

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// This file exercises outputTail directly (package boot, not boot_test) --
// it is unexported and has no external constructor, so a test needing
// newOutputTail(), Write's internal timing, or the afterCR field itself
// must live in-package. It covers the three Batch B4 review findings that
// specifically call for direct outputTail-level coverage (findings 1-3):
// Write's own algorithmic cost, concurrent Write safety, and the afterCR
// CRLF-split carry-over.

// TestIndexLineBoundary_WriteNewlineOnlyLinesScalesLinearly proves finding
// 1: indexLineBoundary must resolve each of a Write's k line boundaries in
// work proportional to that ONE line, not to however much of the buffer is
// still left to scan for '\r' -- a single Write of L newline-only lines
// (no '\r' anywhere, so nothing ever short-circuits a full remaining-buffer
// scan for it) must cost ~O(n), not ~O(k·n).
//
// This is exactly the failure scenario the review measured directly before
// the fix: L=20,000 -> 38.6ms; L=40,000 -> 135.5ms (~3.5x, not ~2x);
// L=80,000 -> 528.5ms (~3.9x again) -- quadratic, not linear.
//
// # Why this measures a RATIO of minima, and no absolute wall-clock bound
//
// The first version of this test also asserted an absolute bound (the 4x
// case must finish under 400ms). That bound made the test flaky the moment
// the machine was busy: it failed at 491ms on a host running other work,
// with the implementation perfectly correct. An absolute wall-clock
// assertion measures the machine, not the code, so it cannot distinguish
// "this regressed to O(k·n)" from "something else was compiling at the
// time" -- and a test that fails for reasons unrelated to its subject stops
// being read as a signal. It is gone.
//
// What remains is self-normalizing: both sizes are measured on the same
// machine moments apart, so a load spike that inflates one tends to inflate
// the other, leaving the RATIO informative where either absolute number is
// not. Each size is measured repeatedly and the MINIMUM taken -- the
// minimum of a set of timings is the estimator least contaminated by
// interference, since scheduling noise, GC and page faults can only ever
// make a run slower, never faster.
func TestIndexLineBoundary_WriteNewlineOnlyLinesScalesLinearly(t *testing.T) {
	measureOnce := func(lines int) time.Duration {
		var buf strings.Builder
		buf.Grow(lines * 11)
		for i := 0; i < lines; i++ {
			buf.WriteString("0123456789\n")
		}
		p := []byte(buf.String())

		tail := newOutputTail()
		start := time.Now()
		if _, err := tail.Write(p); err != nil {
			t.Fatalf("Write() error = %v, want nil", err)
		}
		return time.Since(start)
	}

	// Repeats are cheap here (the largest case is well under a second even
	// quadratically) and buy a much steadier estimate than a single sample.
	const repeats = 5
	measureMin := func(lines int) time.Duration {
		best := measureOnce(lines)
		for i := 1; i < repeats; i++ {
			if d := measureOnce(lines); d < best {
				best = d
			}
		}
		return best
	}

	const small = 20_000
	const large = 4 * small // 80,000 -- same 4x step the review measured

	// Warm up (page faults, allocator warm-up, GC) so the first measured
	// call isn't penalized relative to the rest.
	_ = measureOnce(1_000)

	smallElapsed := measureMin(small)
	largeElapsed := measureMin(large)

	t.Logf("min of %d: Write(%d lines) = %v, Write(%d lines) = %v (ratio %.2fx)",
		repeats, small, smallElapsed, large, largeElapsed, float64(largeElapsed)/float64(smallElapsed))

	// Quadratic scaling over a 4x step lands near 13-16x; linear scaling
	// lands near 4x. 8x sits well clear of both, so this fails hard the
	// instant indexLineBoundary regresses to rescanning the whole remainder
	// for '\r' on every line, without tripping on ordinary measurement
	// noise.
	const maxRatio = 8.0
	if smallElapsed <= 0 {
		t.Fatalf("Write(%d lines) measured as %v -- clock resolution too coarse to compare against", small, smallElapsed)
	}
	if ratio := float64(largeElapsed) / float64(smallElapsed); ratio > maxRatio {
		t.Errorf("Write() time ratio for a 4x input-size step = %.2fx, want under %.1fx (O(k·n) regression?)", ratio, maxRatio)
	}
}

// TestOutputTail_ConcurrentWriteRaceSafe proves finding 2: outputTail's own
// doc comment claims it is "Safe for concurrent Write calls ... guarded by
// a single mutex" (matching runHook's real usage -- one shared *outputTail
// passed as BOTH supervisor.Spec.Stdout and Stderr, read by two independent
// OS-pipe-draining goroutines), yet nothing previously called Write
// concurrently from two goroutines with substantial, overlapping,
// non-trivial payloads. Run with `go test -race`: any future change that
// widens Write's critical section incorrectly, or introduces a second lock,
// must show up here as a data race, not slip through unexercised.
func TestOutputTail_ConcurrentWriteRaceSafe(t *testing.T) {
	tail := newOutputTail()

	const goroutines = 8
	const writesPerGoroutine = 200

	// errgroup.Group, not a bare `go` statement: §11's no-naked-goroutine
	// rule is lint-enforced (tools/lint/narvichecks) and applies to tests
	// too. Nothing here returns a real error -- the assertions are the race
	// detector and the invariants checked after Wait -- so every closure
	// returns nil and Wait's own error is ignored deliberately.
	var group errgroup.Group
	for g := 0; g < goroutines; g++ {
		group.Go(func() error {
			id := g
			for i := 0; i < writesPerGoroutine; i++ {
				// A substantial, varied, multi-line payload per Write (not a
				// single trivial byte) -- including a lone trailing '\r' on
				// some writes so afterCR is ALSO exercised under concurrency,
				// and a CRLF pair on others -- so this stresses the same
				// mutex-guarded state (t.lines, t.cur, t.afterCR) real
				// concurrent stdout/stderr draining would.
				// outputTail.Write never returns an error (documented on the
				// method itself), so every return value here is deliberately
				// discarded rather than asserted on -- errcheck requires the
				// discard be explicit.
				switch i % 4 {
				case 0:
					_, _ = fmt.Fprintf(tail, "goroutine-%d line-%d part-a\npart-b\n", id, i)
				case 1:
					_, _ = fmt.Fprintf(tail, "goroutine-%d progress-%d\r", id, i)
				case 2:
					_, _ = fmt.Fprintf(tail, "goroutine-%d crlf-%d\r\n", id, i)
				default:
					_, _ = tail.Write([]byte(strings.Repeat(strconv.Itoa(id), 37) + "\n"))
				}
			}
			return nil
		})
	}
	_ = group.Wait()

	// No assertion on exact content -- interleaving across goroutines is by
	// definition non-deterministic. The real assertions are (a) -race finds
	// nothing, and (b) the bound still holds under concurrent writers.
	lines := tail.Lines()
	if len(lines) > hookOutputTailMaxLines {
		t.Errorf("Lines() returned %d lines after concurrent writers, want at most %d (bound violated under concurrency)",
			len(lines), hookOutputTailMaxLines)
	}
}

// TestOutputTail_AfterCRCarriesOverSplitCRLF proves finding 3 directly at
// the outputTail level (the boot_test-level TestRunHooks_CRLFNotDoubled
// only ever exercises a CRLF pair delivered in a SINGLE Write call, so it
// cannot and does not exercise the afterCR carry-over branch at all): a
// genuine "\r\n" line ending split across two separate Write calls -- the
// '\r' arriving as the very last byte of one Write, the matching '\n'
// arriving as the very first byte of the next -- must still be captured as
// ONE line boundary, not doubled into an extra blank line.
func TestOutputTail_AfterCRCarriesOverSplitCRLF(t *testing.T) {
	tail := newOutputTail()

	// First Write ends in a lone, unpaired '\r' -- flushed immediately as a
	// line boundary, with afterCR left set so the NEXT Write's leading '\n'
	// (if any) is recognized as the second half of the same CRLF pair.
	if _, err := tail.Write([]byte("first-line\r")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
	if !tail.afterCR {
		t.Fatal("afterCR = false after a Write ending in a lone '\\r', want true")
	}

	// Second Write's leading '\n' is the split CRLF's second half: it must
	// be swallowed, not read as a further (empty) line boundary.
	if _, err := tail.Write([]byte("\nsecond-line\n")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
	if tail.afterCR {
		t.Error("afterCR = true after the following Write consumed it, want false")
	}

	want := []string{"first-line", "second-line"}
	got := tail.Lines()
	if len(got) != len(want) {
		t.Fatalf("Lines() = %v (%d lines), want exactly %v (%d lines) -- a split CRLF must not double into a blank line",
			got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
