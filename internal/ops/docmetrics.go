package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file (docmetrics.go) is the Phase 6 audit's own fix for Finding 2:
// docs/runbooks/README.md and docs/SLOS.md both claimed every metric name
// mentioned in docs/runbooks/*.md is checked against the real registered
// OTel instruments by this package's own CI-enforced drift test — false.
// CheckDrift (drift.go) only ever reads Dashboard.Panels[].Metrics and
// Alert.Metrics, i.e. deploy/observability/*.json; it has never opened a
// single markdown file. TestAlertRunbooksExist (drift_test.go) only
// os.Stats a runbook's path, never its content. LoadGuides (guide.go) is
// scoped to docs/guides/*.md. Two runbook-named metrics —
// session_rollout_refused_total (named in docs/runbooks/README.md and
// sandbox-capability-refusals.md) and cloud_identity_mint_total
// (signing-key-rotation.md) — were therefore real, registered instruments
// that never entered any drift check as input at all: renaming either
// literal would have left this entire test suite green while pointing an
// on-call operator at a dead metric name from a document whose own README
// claims otherwise.
//
// # The mechanism
//
// This is guide.go's own "narvi-command" fenced-JSON-block shape
// (LoadGuides/CheckGuideDrift, docs/guides/*.md), applied to a different
// claim: not "this route/classifier is real" but "this document relies
// on this real OTel metric name" —
//
//	```json narvi-metrics
//	{"metrics": ["outbox_lag_seconds", "outbox_dead_letter_total"]}
//	```
//
// applied to docs/runbooks/*.md (README.md INCLUDED this time — unlike
// docs/guides/README.md's deliberately prose-only, exempted role, this
// directory's own README makes a real, checkable claim about what gets
// checked, so it is not exempted the same way) and docs/SLOS.md (a single
// file, not a one-concern-per-file directory like docs/runbooks/, hence
// LoadDocMetrics below rather than a second LoadRunbookMetrics-shaped
// directory loader).
//
// Unlike LoadGuides, a file with ZERO narvi-metrics blocks is not an
// error here: a runbook whose own failure mode genuinely has no metric
// behind it yet (sandbox-capability-refusals.md's own "No metric exists
// yet" section) has nothing machine-checkable to claim, and forcing an
// empty block onto it would be exactly the "manufactured, not honest"
// shape this whole audit batch exists to remove. What IS still fail
// closed, mirroring LoadGuides' own "never silently skip a bad file"
// discipline: an unterminated fence, malformed JSON inside one, or a
// block naming zero metrics.
//
// metricsFenceOpen/metricsFenceClose delimit one machine-checkable
// "narvi-metrics" block — guide.go's own commandFenceOpen/
// commandFenceClose shape, renamed for this file's own distinct info
// string so the two never collide inside a file that might one day carry
// both kinds of fence.
const (
	metricsFenceOpen  = "```json narvi-metrics"
	metricsFenceClose = "```"
)

// MetricsClaim is one narvi-metrics block's own parsed shape — the unit
// CheckMetricsClaimDrift verifies against a real ScanRegisteredInstruments
// result.
type MetricsClaim struct {
	// Metrics names every OTel instrument the surrounding prose relies
	// on — checked verbatim against ScanRegisteredInstruments' own
	// result, byte for byte, exactly like Dashboard.Panels[].Metrics/
	// Alert.Metrics already are (drift.go).
	Metrics []string `json:"metrics"`

	sourcePath string
	line       int
}

// SourcePath is the file this MetricsClaim was extracted from.
func (c MetricsClaim) SourcePath() string { return c.sourcePath }

// validateMetricsClaim mirrors GuideCommand.Validate's own discipline
// (guide.go): the one structural rule a decoded block must satisfy.
func validateMetricsClaim(c MetricsClaim, sourcePath string, line int) error {
	if len(c.Metrics) == 0 {
		return fmt.Errorf("ops: %s:%d: narvi-metrics block names zero metrics", sourcePath, line)
	}
	for _, m := range c.Metrics {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("ops: %s:%d: narvi-metrics block names an empty metric string", sourcePath, line)
		}
	}
	return nil
}

// extractMetricsBlocks is extractCommandBlocks' (guide.go) own identical
// line-scanner shape, applied to metricsFenceOpen instead of
// commandFenceOpen. Deliberately not factored into one shared helper
// parameterized by fence string and payload type: the two fences bind
// structurally different JSON shapes (GuideCommand's route/classifier
// exactly-one-of pair vs. this file's bare Metrics slice), and Go's own
// lack of a lightweight generic "decode + validate" callback shape here
// would cost more indirection than the ~30 duplicated lines are worth for
// two call sites total.
func extractMetricsBlocks(sourcePath string, content []byte) ([]MetricsClaim, error) {
	lines := strings.Split(string(content), "\n")

	var claims []MetricsClaim
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != metricsFenceOpen {
			continue
		}
		startLine := i + 1 // 1-indexed, the fence-open line itself

		var body strings.Builder
		i++
		closed := false
		for ; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == metricsFenceClose {
				closed = true
				break
			}
			body.WriteString(lines[i])
			body.WriteString("\n")
		}
		if !closed {
			return nil, fmt.Errorf("ops: %s:%d: unterminated %q fence", sourcePath, startLine, metricsFenceOpen)
		}

		var claim MetricsClaim
		dec := json.NewDecoder(strings.NewReader(body.String()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&claim); err != nil {
			return nil, fmt.Errorf("ops: %s:%d: malformed narvi-metrics JSON: %w", sourcePath, startLine, err)
		}
		if err := validateMetricsClaim(claim, sourcePath, startLine); err != nil {
			return nil, err
		}
		claim.sourcePath = sourcePath
		claim.line = startLine

		claims = append(claims, claim)
	}

	return claims, nil
}

// LoadRunbookMetrics reads every "*.md" file directly inside dir —
// README.md INCLUDED, see this file's own top comment for why — in
// deterministic (sorted-by-filename) order, and extracts every
// narvi-metrics block from each. A file with no block at all is not an
// error (see this file's own top comment); a malformed one always is.
func LoadRunbookMetrics(dir string) ([]MetricsClaim, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("ops: read dir %s: %w", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)

	var out []MetricsClaim
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("ops: read %s: %w", p, err)
		}
		claims, err := extractMetricsBlocks(p, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, claims...)
	}
	return out, nil
}

// LoadDocMetrics reads ONE markdown file — docs/SLOS.md's own use case, a
// single file rather than a directory of one-concern-per-file entries
// like docs/runbooks/ — and extracts every narvi-metrics block inside it.
func LoadDocMetrics(path string) ([]MetricsClaim, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ops: read %s: %w", path, err)
	}
	return extractMetricsBlocks(path, raw)
}

// CheckMetricsClaimDrift is CheckDrift's (drift.go) own shape applied to
// claims instead of dashboards/alerts: one DriftError (Kind "doc") per
// metric name a narvi-metrics block references that
// ScanRegisteredInstruments' own result does not contain. A nil/empty
// result means every fenced claim names only metrics the code actually
// registers — the CI-passing state.
func CheckMetricsClaimDrift(claims []MetricsClaim, registered map[string]RegisteredInstrument) []DriftError {
	var errs []DriftError
	for _, c := range claims {
		for _, m := range c.Metrics {
			if _, ok := registered[m]; !ok {
				errs = append(errs, DriftError{Kind: "doc", Source: c.SourcePath(), Item: c.SourcePath(), Metric: m})
			}
		}
	}
	return errs
}
