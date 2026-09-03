package shadowledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// fakeStore mirrors internal/app/shadowscm/decorator_test.go's own fake of
// the identical name -- a store that either records the insert or fails on
// demand, which is the one property record-or-fail actually needs a test
// to exercise.
type fakeStore struct {
	rows []sqlcgen.CreateShadowSCMWriteParams
	err  error
}

func (s *fakeStore) Create(_ context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error) {
	if s.err != nil {
		return sqlcgen.ShadowScmWrite{}, s.err
	}
	s.rows = append(s.rows, arg)
	return sqlcgen.ShadowScmWrite{}, nil
}

// TestRecord_ScmCredentialMintRefused proves the §30.4(4) refusal record
// round-trips through Record with the SAME record-or-fail semantics every
// other spec in this package already gets, and that the marshalled
// spec_json carries no token -- there is no field to put one in (Spec's
// own doc comment), but this pins that ScmCredentialMintRefused
// specifically, rather than trusting the sealed interface alone to prove
// it for a type nothing here exercises.
func TestRecord_ScmCredentialMintRefused(t *testing.T) {
	store := &fakeStore{}
	err := Record(context.Background(), store, Entry{
		Operation:    "scm_credential_mint_refused",
		RepoFullName: "acme/widgets",
		Target:       "github.com",
		Spec: ScmCredentialMintRefused{
			Host:               "github.com",
			Reason:             `permission "contents" is "write", not read-only`,
			GrantedPermissions: map[string]string{"contents": "write", "metadata": "read"},
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v, want nil", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("store recorded %d rows, want 1", len(store.rows))
	}

	row := store.rows[0]
	if row.Operation != "scm_credential_mint_refused" {
		t.Errorf("row.Operation = %q, want %q", row.Operation, "scm_credential_mint_refused")
	}
	if row.RepoFullName != "acme/widgets" {
		t.Errorf("row.RepoFullName = %q, want %q", row.RepoFullName, "acme/widgets")
	}
	if row.ResultJson != nil {
		t.Errorf("row.ResultJson = %s, want nil (nothing was invented in place of a refused mint)", row.ResultJson)
	}

	var decoded ScmCredentialMintRefused
	if err := json.Unmarshal(row.SpecJson, &decoded); err != nil {
		t.Fatalf("unmarshal row.SpecJson: %v", err)
	}
	if decoded.Host != "github.com" || decoded.GrantedPermissions["contents"] != "write" {
		t.Errorf("decoded spec = %+v, want the original ScmCredentialMintRefused", decoded)
	}
	if strings.Contains(string(row.SpecJson), "ghs_") || strings.Contains(string(row.SpecJson), "token") {
		t.Errorf("row.SpecJson = %s, must never carry anything token-shaped", row.SpecJson)
	}
}

// TestRecord_UpdateFileContentSplitsHeavyContent pins migrations/
// 000110_shadow_scm_writes_heavy_content.up.sql's own contract: a
// customer file's own content reaches heavy_content, and ONLY there --
// spec_json must carry every other UpdateFileContent field but never the
// content itself, so a future retention null-out of that one column
// removes the content and nothing else.
func TestRecord_UpdateFileContentSplitsHeavyContent(t *testing.T) {
	const content = "package main\n\nfunc secretSauce() {}\n"
	store := &fakeStore{}
	err := Record(context.Background(), store, Entry{
		Operation:    "update_file_content",
		RepoFullName: "acme/widgets",
		Target:       "main.go",
		Spec: UpdateFileContent{
			Owner:   "acme",
			Repo:    "widgets",
			Path:    "main.go",
			Content: content,
			SHA:     "deadbeef",
			Branch:  "feature/x",
			Message: "update main.go",
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v, want nil", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("store recorded %d rows, want 1", len(store.rows))
	}
	row := store.rows[0]

	if row.HeavyContent == nil || *row.HeavyContent != content {
		t.Fatalf("row.HeavyContent = %v, want %q", row.HeavyContent, content)
	}
	if strings.Contains(string(row.SpecJson), "secretSauce") {
		t.Errorf("row.SpecJson = %s, must never carry the file content -- it belongs in heavy_content alone", row.SpecJson)
	}

	var decoded UpdateFileContent
	if err := json.Unmarshal(row.SpecJson, &decoded); err != nil {
		t.Fatalf("unmarshal row.SpecJson: %v", err)
	}
	if decoded.Content != "" {
		t.Errorf("decoded.Content = %q, want empty -- the spec_json copy must be blanked", decoded.Content)
	}
	if decoded.Path != "main.go" || decoded.SHA != "deadbeef" || decoded.Branch != "feature/x" {
		t.Errorf("decoded spec = %+v, every non-content field must still round-trip through spec_json", decoded)
	}
}

// TestRecord_NonFileContentSpecsCarryNoHeavyContent proves the split is
// scoped to UpdateFileContent alone: every other spec type's row leaves
// heavy_content NULL, never an empty string standing in for "no content".
func TestRecord_NonFileContentSpecsCarryNoHeavyContent(t *testing.T) {
	store := &fakeStore{}
	err := Record(context.Background(), store, Entry{
		Operation:    "create_branch",
		RepoFullName: "acme/widgets",
		Target:       "feature/x",
		Spec:         CreateBranch{Owner: "acme", Repo: "widgets", Branch: "feature/x", SHA: "deadbeef"},
	})
	if err != nil {
		t.Fatalf("Record() error = %v, want nil", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("store recorded %d rows, want 1", len(store.rows))
	}
	if got := store.rows[0].HeavyContent; got != nil {
		t.Errorf("row.HeavyContent = %v, want nil for a non-UpdateFileContent spec", *got)
	}
}

// TestRecord_FailsLoudlyWhenTheStoreCannotWrite is the record-or-fail
// property itself (this package's own top doc comment): a suppression
// (or, here, a refusal) that cannot be recorded must surface as an error,
// never be reported as if it succeeded.
func TestRecord_FailsLoudlyWhenTheStoreCannotWrite(t *testing.T) {
	store := &fakeStore{err: errors.New("boom: connection reset")}
	err := Record(context.Background(), store, Entry{
		Operation:    "scm_credential_mint_refused",
		RepoFullName: "acme/widgets",
		Spec:         ScmCredentialMintRefused{Host: "github.com", Reason: "test"},
	})
	if err == nil {
		t.Fatal("Record() error = nil, want an error when the store itself fails to write")
	}
}

// eventAppenderStore is fakeStore plus §30.6's optional timeline half.
type eventAppenderStore struct {
	fakeStore
	events   []sqlcgen.CreateEventParams
	eventErr error
}

func (s *eventAppenderStore) AppendSuppressionEvent(_ context.Context, sessionID pgtype.UUID, messageID string, payload []byte) error {
	if s.eventErr != nil {
		return s.eventErr
	}
	s.events = append(s.events, sqlcgen.CreateEventParams{
		SessionID: sessionID, Type: "shadow_egress_suppressed", MessageID: messageID, Payload: payload,
	})
	return nil
}

// TestRecord_AppendsTheSuppressionTimelineEvent covers §30.6's third
// recording write, which both the section and the plan row said had
// shipped and which existed nowhere in the code for the whole phase.
//
// Without it an operator watching a session's workspace — the surface
// §30.6 names, and the one they are already looking at — sees a turn
// where nothing happened.
func TestRecord_AppendsTheSuppressionTimelineEvent(t *testing.T) {
	store := &eventAppenderStore{}
	sessionID := pgtype.UUID{Bytes: [16]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x47, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x01}, Valid: true}

	if err := Record(context.Background(), store, Entry{
		Operation:    "create_pr",
		RepoFullName: "acme/widgets",
		Target:       "feature-x",
		SessionID:    sessionID,
		Spec:         CreatePR{Owner: "acme", Repo: "widgets", Head: "feature-x", Base: "main"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if len(store.events) != 1 {
		t.Fatalf("timeline events = %d, want 1: a suppression must appear inline in the session workspace (§30.6)", len(store.events))
	}
	ev := store.events[0]
	if ev.Type != "shadow_egress_suppressed" {
		t.Errorf("event type = %q, want %q", ev.Type, "shadow_egress_suppressed")
	}
	if ev.SessionID != sessionID {
		t.Errorf("event session = %v, want the suppression's own session %v", ev.SessionID, sessionID)
	}
	payload := string(ev.Payload)
	if !strings.Contains(payload, "create_pr") || !strings.Contains(payload, "acme/widgets") {
		t.Errorf("payload = %s, want the operation and repo", payload)
	}
	if !strings.Contains(payload, "ledgerId") {
		t.Errorf("payload = %s, want the ledger id §30.6 names", payload)
	}
}

// TestRecord_TimelineFailureDoesNotFailTheSuppression pins the line §30.6
// itself draws: the ledger row is the record and is record-or-fail; the
// events row is surface that cascades with the session. Failing the whole
// suppression because a timeline entry did not land would trade the
// durable half for the disposable one.
func TestRecord_TimelineFailureDoesNotFailTheSuppression(t *testing.T) {
	store := &eventAppenderStore{eventErr: errors.New("simulated events failure")}

	if err := Record(context.Background(), store, Entry{
		Operation:    "create_pr",
		RepoFullName: "acme/widgets",
		SessionID:    pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true},
		Spec:         CreatePR{Owner: "acme", Repo: "widgets"},
	}); err != nil {
		t.Fatalf("Record() error = %v, want nil: the ledger row committed, and the timeline is surface", err)
	}
	if len(store.rows) != 1 {
		t.Errorf("ledger rows = %d, want 1", len(store.rows))
	}
}
