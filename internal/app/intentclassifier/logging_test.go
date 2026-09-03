package intentclassifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/app/ports"
	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// captureDefaultLoggerJSON temporarily replaces slog.Default() with a JSON
// handler writing into a *bytes.Buffer, restoring the original on
// cleanup -- mirrors internal/app/sessionactor/planrecord_integration_test.go's
// own established "capture slog.Default() into a buffer" convention
// (itself reused from internal/app/outboxworker/builder_integration_test.go).
// platform.Logger(ctx) resolves slog.Default() on every call (it is not
// cached anywhere in this package), so, unlike that convention's own
// "install before hydrate" caveat, install order relative to Classify
// doesn't matter here.
func captureDefaultLoggerJSON(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLogger) })
	return &buf
}

// findLogEntries scans buf's own newline-delimited JSON log lines for
// every one whose "msg" field equals wantMsg.
func findLogEntries(t *testing.T, buf *bytes.Buffer, wantMsg string) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if entry["msg"] == wantMsg {
			entries = append(entries, entry)
		}
	}
	return entries
}

// TestService_Classify_LogsDistinctMessagePerFallbackBranch is the H9
// audit fix's own required coverage: each of Classify's 4 internal
// fallback branches (template fetch failure, template assemble failure, a
// real *ports.LLMError, invalid/unparseable LLM output) must log its OWN
// distinct, identifiable message -- never a single generic "something went
// wrong" line an operator can't tell apart -- carrying the real underlying
// error where one exists.
func TestService_Classify_LogsDistinctMessagePerFallbackBranch(t *testing.T) {
	tests := []struct {
		name          string
		llm           *fakeLLM
		templates     *fakeTemplates
		wantMsg       string
		wantBranch    string
		wantErrSubstr string // substring the "error" (or "unmarshal_error") attr must contain, "" to skip
		wantRawSubstr string // substring "raw_output" must contain, "" to skip
	}{
		{
			name:          "template fetch failure",
			llm:           &fakeLLM{},
			templates:     &fakeTemplates{err: errors.New("db unreachable")},
			wantMsg:       "intentclassifier: template fetch failed, falling back",
			wantBranch:    fallbackBranchTemplateFetch,
			wantErrSubstr: "db unreachable",
		},
		{
			name:          "template assemble failure",
			llm:           &fakeLLM{},
			templates:     brokenTemplates(),
			wantMsg:       "intentclassifier: template assemble failed, falling back",
			wantBranch:    fallbackBranchTemplateAssemble,
			wantErrSubstr: "this_placeholder_does_not_exist",
		},
		{
			name:          "real *ports.LLMError",
			llm:           &fakeLLM{err: &ports.LLMError{Code: ports.CodeTimeout, Provider: "anthropic"}},
			templates:     validTemplates(),
			wantMsg:       "intentclassifier: llm call failed, falling back",
			wantBranch:    fallbackBranchLLMError,
			wantErrSubstr: "timeout",
		},
		{
			name:          "invalid/unparseable LLM output",
			llm:           &fakeLLM{response: json.RawMessage(`not valid json`)},
			templates:     validTemplates(),
			wantMsg:       "intentclassifier: llm returned invalid output, falling back",
			wantBranch:    fallbackBranchInvalidOutput,
			wantRawSubstr: "not valid json",
		},
	}

	// Every branch's own message must be distinct from every other
	// branch's -- asserted once, up front, against the literal fixture
	// table above (rather than per-subtest), so a future edit that
	// accidentally collapses two branches onto the same message text
	// fails loudly here.
	seen := make(map[string]string, len(tests))
	for _, tt := range tests {
		if other, ok := seen[tt.wantMsg]; ok {
			t.Fatalf("fixture bug: branches %q and %q share the same message %q", other, tt.name, tt.wantMsg)
		}
		seen[tt.wantMsg] = tt.name
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureDefaultLoggerJSON(t)

			svc := New(tt.llm, "anthropic", "claude-haiku-4-5", tt.templates, nil, nil)
			decision := svc.Classify(context.Background(), ports.IntentClassifierInput{Text: "hello", Surface: "web"})

			if decision.Source != ports.IntentSourceFallback {
				t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceFallback)
			}

			entries := findLogEntries(t, buf, tt.wantMsg)
			if len(entries) != 1 {
				t.Fatalf("found %d log entries with msg %q, want exactly 1 (full log output: %s)", len(entries), tt.wantMsg, buf.String())
			}
			entry := entries[0]

			if entry["level"] != "WARN" {
				t.Errorf("level = %v, want WARN", entry["level"])
			}
			if entry["fallback_branch"] != tt.wantBranch {
				t.Errorf("fallback_branch = %v, want %q", entry["fallback_branch"], tt.wantBranch)
			}
			if entry["surface"] != "web" {
				t.Errorf("surface = %v, want %q", entry["surface"], "web")
			}
			if tt.wantErrSubstr != "" {
				errAttr, _ := entry["error"].(string)
				if errAttr == "" {
					errAttr, _ = entry["unmarshal_error"].(string)
				}
				if !strings.Contains(errAttr, tt.wantErrSubstr) {
					t.Errorf("error/unmarshal_error attr = %q, want substring %q", errAttr, tt.wantErrSubstr)
				}
			}
			if tt.wantRawSubstr != "" {
				rawAttr, _ := entry["raw_output"].(string)
				if !strings.Contains(rawAttr, tt.wantRawSubstr) {
					t.Errorf("raw_output attr = %q, want substring %q", rawAttr, tt.wantRawSubstr)
				}
			}
		})
	}
}

// TestService_Classify_LogsInfoOnSuccess proves the success-path
// counterpart of the fix above: a genuine classifier verdict logs an Info
// line naming the surface, whether a deterministic target was supplied,
// and the verdict itself (target/mode/confidence/source) -- previously
// Classify logged NOTHING at all on ANY path, success included.
func TestService_Classify_LogsInfoOnSuccess(t *testing.T) {
	buf := captureDefaultLoggerJSON(t)

	llm := &fakeLLM{response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "clear ask to review")}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)

	decision := svc.Classify(context.Background(), ports.IntentClassifierInput{
		Text:                "please review this PR",
		Surface:             "github",
		DeterministicTarget: intentdomain.TargetReview,
	})
	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
	}

	entries := findLogEntries(t, buf, "intentclassifier: classified")
	if len(entries) != 1 {
		t.Fatalf("found %d log entries with msg %q, want exactly 1 (full log output: %s)", len(entries), "intentclassifier: classified", buf.String())
	}
	entry := entries[0]

	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if entry["surface"] != "github" {
		t.Errorf("surface = %v, want %q", entry["surface"], "github")
	}
	if entry["has_deterministic_target"] != true {
		t.Errorf("has_deterministic_target = %v, want true", entry["has_deterministic_target"])
	}
	if entry["target"] != intentdomain.TargetReview {
		t.Errorf("target = %v, want %q", entry["target"], intentdomain.TargetReview)
	}
	if entry["mode"] != intentdomain.ModeBuild {
		t.Errorf("mode = %v, want %q", entry["mode"], intentdomain.ModeBuild)
	}
	if entry["confidence"] != intentdomain.ConfidenceHigh {
		t.Errorf("confidence = %v, want %q", entry["confidence"], intentdomain.ConfidenceHigh)
	}
	if entry["source"] != ports.IntentSourceClassifier {
		t.Errorf("source = %v, want %q", entry["source"], ports.IntentSourceClassifier)
	}
}
