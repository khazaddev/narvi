package intentclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/app/ports"
	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// erroringSessionStore is a DecisionStore fake whose
// UpdateIntentDecisionIfNull always fails -- used to exercise
// ClassifyAndRecord's own "log and otherwise ignore a RecordDecision
// failure" branch without needing a real Postgres error.
type erroringSessionStore struct {
	err error
}

func (e *erroringSessionStore) UpdateIntentDecisionIfNull(_ context.Context, _ pgtype.UUID, _ []byte) (bool, error) {
	return false, e.err
}

// TestService_ClassifyAndRecord_ClassifierSuccess_RecordsDecision proves
// the shared H9/L11 helper, on a genuine classifier verdict, records a
// decision with Confidence/Reasoning populated (non-nil) and the caller-
// supplied stage carried through untouched.
func TestService_ClassifyAndRecord_ClassifierSuccess_RecordsDecision(t *testing.T) {
	llm := &fakeLLM{response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "clear ask to review")}
	sessions := newFakeSessionStore()
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	id := testSessionID()
	decision := svc.ClassifyAndRecord(context.Background(), id, ports.IntentClassifierInput{
		Text:    "please review this PR",
		Surface: "github",
	}, intentdomain.DecidedAtStageCreate)

	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceClassifier)
	}
	if decision.Target != intentdomain.TargetReview {
		t.Errorf("Target = %q, want %q", decision.Target, intentdomain.TargetReview)
	}

	if !sessions.set[id] {
		t.Fatal("RecordDecision was never called (or never won) for this session id")
	}

	// A second ClassifyAndRecord call for the SAME session id must NOT
	// win (write-once) -- proves ClassifyAndRecord genuinely calls
	// through to RecordDecision rather than some other, parallel path.
	won2, err := svc.RecordDecision(context.Background(), id, intentdomain.IntentDecisionRecord{
		Surface:        "github",
		Source:         intentdomain.RecordSourceClassifier,
		Target:         intentdomain.TargetReview,
		Mode:           intentdomain.ModeBuild,
		DecidedAtStage: intentdomain.DecidedAtStageCreate,
	})
	if err != nil {
		t.Fatalf("RecordDecision (verify) error = %v, want nil", err)
	}
	if won2 {
		t.Error("RecordDecision (verify) won = true, want false (ClassifyAndRecord's own call should have already won)")
	}
}

// TestService_ClassifyAndRecord_Fallback_NilConfidenceAndReasoning proves
// a fallback decision (never a genuine classifier verdict) records
// Confidence/Reasoning as nil -- exactly §18.4's own nullability rule,
// mirroring TestService_Classify_NeverThrows' own coverage of Classify
// itself, now exercised through the shared helper.
func TestService_ClassifyAndRecord_Fallback_NilConfidenceAndReasoning(t *testing.T) {
	sessions := newFakeSessionStore()
	svc := New(&fakeLLM{err: errors.New("boom")}, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	id := testSessionID()
	decision := svc.ClassifyAndRecord(context.Background(), id, ports.IntentClassifierInput{
		Text:    "hello",
		Surface: "slack",
	}, intentdomain.DecidedAtStageFirstPrompt)

	if decision.Source != ports.IntentSourceFallback {
		t.Fatalf("Source = %q, want %q", decision.Source, ports.IntentSourceFallback)
	}
	if !sessions.set[id] {
		t.Fatal("RecordDecision was never called (or never won) for this session id")
	}
}

// TestService_ClassifyAndRecord_RecordFailure_LogsWarnAndNeverPanics
// proves a genuine RecordDecision failure (a real database error, faked
// here via erroringSessionStore) is logged at Warn -- prefixed with
// input.Surface, exactly matching every pre-existing per-surface call
// site's own former log text -- and never propagated to the caller (§18.1
// never-throw-adjacent discipline this whole step already follows).
func TestService_ClassifyAndRecord_RecordFailure_LogsWarnAndNeverPanics(t *testing.T) {
	buf := captureDefaultLoggerJSON(t)

	wantErr := errors.New("connection reset by peer")
	llm := &fakeLLM{response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "x")}
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), &erroringSessionStore{err: wantErr}, nil)

	id := testSessionID()
	decision := svc.ClassifyAndRecord(context.Background(), id, ports.IntentClassifierInput{
		Text:    "please review",
		Surface: "linear",
	}, intentdomain.DecidedAtStageCreate)

	if decision.Source != ports.IntentSourceClassifier {
		t.Fatalf("Source = %q, want %q (a RecordDecision failure must not change the returned decision)", decision.Source, ports.IntentSourceClassifier)
	}

	entries := findLogEntries(t, buf, "linear: record intent decision failed")
	if len(entries) != 1 {
		t.Fatalf("found %d log entries with msg %q, want exactly 1 (full log output: %s)", len(entries), "linear: record intent decision failed", buf.String())
	}
	if entries[0]["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entries[0]["level"])
	}
	errAttr, _ := entries[0]["error"].(string)
	if errAttr != wantErr.Error() {
		t.Errorf("error attr = %q, want %q", errAttr, wantErr.Error())
	}
}

// TestService_ClassifyAndRecord_ClassifierSuccess_RecordsCostUSD is the
// L18 audit fix's own end-to-end coverage: a genuine classifier verdict
// whose underlying ports.LLM.Complete call reports a real
// CompletionResponse.CostUSD flows, unchanged, all the way from Classify's
// own ports.IntentDecision.CostUSD into the intentdomain.
// IntentDecisionRecord ClassifyAndRecord actually persists -- the exact
// field IntentDecisionRecord.CostUSD's own doc comment says stays nil
// "omitted, never guessed" until something actually populates it. Asserts
// against the real persisted payload (via fakeSessionStore's own captured
// bytes), not just the returned decision, so this test would catch a bug
// where CostUSD reaches Classify's return value but never makes it into
// the record ClassifyAndRecord builds for RecordDecision.
func TestService_ClassifyAndRecord_ClassifierSuccess_RecordsCostUSD(t *testing.T) {
	cost := 0.00011 // see TestAnthropicAdapter_Complete_Success_ReportsUsageAndCost for the arithmetic this figure mirrors
	llm := &fakeLLM{
		response: successResponse(intentdomain.TargetReview, intentdomain.ModeBuild, intentdomain.ConfidenceHigh, "clear ask to review"),
		costUSD:  &cost,
	}
	sessions := newFakeSessionStore()
	svc := New(llm, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	id := testSessionID()
	decision := svc.ClassifyAndRecord(context.Background(), id, ports.IntentClassifierInput{
		Text:    "please review this PR",
		Surface: "github",
	}, intentdomain.DecidedAtStageCreate)

	if decision.CostUSD == nil || *decision.CostUSD != cost {
		t.Fatalf("decision.CostUSD = %v, want %v", decision.CostUSD, cost)
	}

	payload, ok := sessions.payloads[id]
	if !ok {
		t.Fatal("no decision payload recorded for this session id")
	}
	var rec intentdomain.IntentDecisionRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatalf("unmarshal recorded payload: %v", err)
	}
	if rec.CostUSD == nil || *rec.CostUSD != cost {
		t.Fatalf("recorded IntentDecisionRecord.CostUSD = %v, want %v", rec.CostUSD, cost)
	}
}

// TestService_ClassifyAndRecord_Fallback_CostUSDNil proves a fallback
// decision (no real LLM call succeeded, so there is no real cost to
// report) records CostUSD as nil in the actual persisted
// IntentDecisionRecord -- exactly that field's own "omitted, never
// guessed" contract, mirroring
// TestService_ClassifyAndRecord_Fallback_NilConfidenceAndReasoning's own
// coverage of Confidence/Reasoning.
func TestService_ClassifyAndRecord_Fallback_CostUSDNil(t *testing.T) {
	sessions := newFakeSessionStore()
	svc := New(&fakeLLM{err: errors.New("boom")}, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	id := testSessionID()
	decision := svc.ClassifyAndRecord(context.Background(), id, ports.IntentClassifierInput{
		Text:    "hello",
		Surface: "slack",
	}, intentdomain.DecidedAtStageFirstPrompt)

	if decision.CostUSD != nil {
		t.Errorf("decision.CostUSD = %v, want nil for a fallback decision", *decision.CostUSD)
	}

	payload, ok := sessions.payloads[id]
	if !ok {
		t.Fatal("no decision payload recorded for this session id")
	}
	var rec intentdomain.IntentDecisionRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatalf("unmarshal recorded payload: %v", err)
	}
	if rec.CostUSD != nil {
		t.Errorf("recorded IntentDecisionRecord.CostUSD = %v, want nil", *rec.CostUSD)
	}
}
