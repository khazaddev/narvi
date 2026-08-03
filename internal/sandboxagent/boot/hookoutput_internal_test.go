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
// # Why this measures TWO independent step ratios, not one
//
// The first version of this test asserted an absolute bound (the 4x case
// must finish under 400ms) -- flaky the moment the machine was busy: it
// failed at 491ms on a host running other work, with the implementation
// perfectly correct. A second version replaced that with a single ratio
// over one 4x step (20k -> 80k, minimum of several repeats each) -- still
// occasionally flaky under REAL CI contention, not just a busy dev laptop:
// CI run 30831633470 measured this same, unchanged, genuinely-linear
// implementation at 9.84x against that version's 8.0x threshold, while a
// wholly unrelated package's own test was independently hanging the whole
// runner for ten minutes on a stuck Docker call in the same job -- entirely
// plausible contention for this test's own measurement window to land in.
//
// A single ratio over one step cannot tell "the algorithm regressed" apart
// from "one of the two measurements got unlucky". Per this test's own
// existing reasoning below (the MINIMUM of several repeats, since
// scheduling noise/GC/page faults can only ever make a run slower, never
// faster), an unlucky measurement always moves the SAME direction: slower.
// Three sizes across two independent 2x steps (small->mid, mid->large)
// closes this: mid is the only measurement shared by both steps -- the
// numerator of one and the denominator of the other -- so inflating mid
// alone pushes step1 (mid/small) UP but step2 (large/mid) DOWN, and
// inflating small or large alone affects only ONE step, leaving the other
// unchanged. A single noisy sample can therefore only ever push ONE of the
// two step ratios toward false failure, never both at once. A genuine
// O(k·n) regression does the opposite: every size scales quadratically
// relative to its predecessor, so BOTH steps land near the same elevated
// ratio, consistently. Requiring BOTH independent steps to breach threshold
// before failing catches a real regression exactly as reliably as the old
// single-ratio check, while making the actual CI-observed failure mode --
// one sample, one step, unlucky -- structurally unable to fail the test on
// its own.
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
	const mid = 2 * small // 40,000 -- first 2x step
	const large = 2 * mid // 80,000 -- second 2x step; same 4x-from-small the review originally measured overall

	// Warm up (page faults, allocator warm-up, GC) so the first measured
	// call isn't penalized relative to the rest.
	_ = measureOnce(1_000)

	smallElapsed := measureMin(small)
	midElapsed := measureMin(mid)
	largeElapsed := measureMin(large)

	if smallElapsed <= 0 || midElapsed <= 0 {
		t.Fatalf("Write(%d lines) = %v, Write(%d lines) = %v -- clock resolution too coarse to compare against",
			small, smallElapsed, mid, midElapsed)
	}
	step1 := float64(midElapsed) / float64(smallElapsed)
	step2 := float64(largeElapsed) / float64(midElapsed)

	t.Logf("min of %d: Write(%d)=%v Write(%d)=%v Write(%d)=%v (step1 %.2fx, step2 %.2fx)",
		repeats, small, smallElapsed, mid, midElapsed, large, largeElapsed, step1, step2)

	// Quadratic scaling over a 2x step lands near 4x; linear scaling lands
	// near 2x. 3.0 sits clear of both while leaving real headroom for
	// per-measurement noise -- and, per this test's own doc comment above,
	// BOTH independent steps must breach it before this fails, not just
	// one.
	const maxStepRatio = 3.0
	if step1 > maxStepRatio && step2 > maxStepRatio {
		t.Errorf("Write() time ratio exceeded %.1fx on BOTH independent 2x input-size steps "+
			"(%d->%d = %.2fx, %d->%d = %.2fx) -- O(k·n) regression?",
			maxStepRatio, small, mid, step1, mid, large, step2)
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
