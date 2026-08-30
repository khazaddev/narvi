package shadowlinear

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

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

// liveSpy counts every call it receives. In shadow tests (failOnWrite
// true) it also fails the test the instant a write reaches it. GetUserEmail
// is always allowed, and counted, because reads pass through regardless
// of mode.
type liveSpy struct {
	t           *testing.T
	failOnWrite bool
	readCalls   int
	writeCalls  int
}

func (l *liveSpy) GetUserEmail(context.Context, string, string) (string, error) {
	l.readCalls++
	return "actor@example.com", nil
}
func (l *liveSpy) CreateThoughtActivity(context.Context, string, string, string, string) error {
	l.writeCalls++
	if l.failOnWrite {
		l.t.Error("CreateThoughtActivity reached the live client in shadow")
	}
	return nil
}
func (l *liveSpy) CreateResponseActivity(context.Context, string, string, string, string) error {
	l.writeCalls++
	if l.failOnWrite {
		l.t.Error("CreateResponseActivity reached the live client in shadow")
	}
	return nil
}

func shadowDecorator(t *testing.T, store *fakeStore) (*Decorator, *liveSpy) {
	t.Helper()
	spy := &liveSpy{t: t, failOnWrite: true}
	d, err := New(spy, store, "acme/widgets", func(context.Context, string) bool { return false })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, spy
}

// TestDecorator_WritesAreSuppressedAndRecorded proves both mutation
// methods record into the ledger instead of reaching the live client,
// with the repository this Decorator was built for attributed on every
// row, and the access token excluded.
func TestDecorator_WritesAreSuppressedAndRecorded(t *testing.T) {
	store := &fakeStore{}
	d, spy := shadowDecorator(t, store)
	ctx := context.Background()

	if err := d.CreateThoughtActivity(ctx, "linear-installation-token", "agent-session-1", "Narvi has started working on this.", ""); err != nil {
		t.Fatalf("CreateThoughtActivity: %v", err)
	}
	if err := d.CreateResponseActivity(ctx, "linear-installation-token", "agent-session-1", "Approved.", ""); err != nil {
		t.Fatalf("CreateResponseActivity: %v", err)
	}

	if spy.writeCalls != 0 {
		t.Fatalf("live writeCalls = %d, want 0 -- a single leaked write is total failure of this capability", spy.writeCalls)
	}
	if len(store.rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2", len(store.rows))
	}
	wantOps := []string{"linear_create_thought_activity", "linear_create_response_activity"}
	for i, row := range store.rows {
		if row.Operation != wantOps[i] {
			t.Errorf("row %d Operation = %q, want %q", i, row.Operation, wantOps[i])
		}
		if row.RepoFullName != "acme/widgets" {
			t.Errorf("row %d RepoFullName = %q, want %q", i, row.RepoFullName, "acme/widgets")
		}
		if strings.Contains(string(row.SpecJson), "linear-installation-token") {
			t.Errorf("the access token reached the ledger: %s", row.SpecJson)
		}
	}
}

// TestDecorator_LiveRepoPassesThrough proves the decorator is not simply
// blocking everything: shadow mode must be graduable.
func TestDecorator_LiveRepoPassesThrough(t *testing.T) {
	store := &fakeStore{}
	spy := &liveSpy{t: t}
	d, err := New(spy, store, "acme/widgets", func(context.Context, string) bool { return true })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := d.CreateThoughtActivity(context.Background(), "token", "agent-session-1", "hi", ""); err != nil {
		t.Fatalf("CreateThoughtActivity: %v", err)
	}
	if spy.writeCalls != 1 {
		t.Errorf("live writeCalls = %d, want 1", spy.writeCalls)
	}
	if len(store.rows) != 0 {
		t.Errorf("a live write was recorded in the suppression ledger (%d rows)", len(store.rows))
	}
}

// TestDecorator_ReadsAreForwarded: shadow must not blind identity
// auto-linking.
func TestDecorator_ReadsAreForwarded(t *testing.T) {
	store := &fakeStore{}
	d, spy := shadowDecorator(t, store)

	if _, err := d.GetUserEmail(context.Background(), "token", "U1"); err != nil {
		t.Fatalf("GetUserEmail: %v", err)
	}
	if spy.readCalls != 1 {
		t.Errorf("read forwarded %d times, want 1", spy.readCalls)
	}
	if len(store.rows) != 0 {
		t.Errorf("a read was recorded as a suppressed write (%d rows)", len(store.rows))
	}
}

// TestDecorator_LedgerFailureFailsTheWrite pins record-or-fail: a
// suppression that could not be recorded must not be reported as one.
func TestDecorator_LedgerFailureFailsTheWrite(t *testing.T) {
	d, _ := shadowDecorator(t, &fakeStore{err: errors.New("ledger down")})

	if err := d.CreateThoughtActivity(context.Background(), "token", "agent-session-1", "hi", ""); err == nil {
		t.Fatal("CreateThoughtActivity reported success when its ledger insert failed")
	}
}

// TestNew_RefusesAnIncompleteConstruction: there is no convenience default
// that yields a pass-through, because a pass-through obtained by omission
// is the failure this layer exists to remove.
func TestNew_RefusesAnIncompleteConstruction(t *testing.T) {
	live := &liveSpy{t: t}
	store := &fakeStore{}
	isLive := func(context.Context, string) bool { return true }

	if _, err := New(nil, store, "acme/widgets", isLive); err == nil {
		t.Error("New accepted a nil live client")
	}
	if _, err := New(live, nil, "acme/widgets", isLive); err == nil {
		t.Error("New accepted a nil ledger")
	}
	if _, err := New(live, store, "", isLive); err == nil {
		t.Error("New accepted an empty repoFullName -- the ledger is read per repository")
	}
	if _, err := New(live, store, "acme/widgets", nil); err == nil {
		t.Error("New accepted a nil resolver -- which would make live/shadow undefined")
	}
}

// TestDecorator_IdentityNotice_NeverRecordsItsText mirrors the guarantee
// the Slack seam already had, on the half that was missed.
//
// The identity-link prompt carries a live magic-link URL whose nonce is
// credential-equivalent: whoever holds it can bind a Linear identity to a
// Narvi account. It used to be concatenated into the activity body by the
// caller, and the shadow decorator records bodies verbatim into a
// permanent, append-only table — so a shadow deployment stored a working
// nonce for every unlinked Linear actor, for the full prompt TTL.
//
// The prompt now travels as its own parameter. The assertion is on the
// SERIALISED row, not the struct's fields: a field list can be read and
// believed, only the bytes prove nothing leaked.
func TestDecorator_IdentityNotice_NeverRecordsItsText(t *testing.T) {
	store := &fakeStore{}
	d, _ := shadowDecorator(t, store)

	const nonce = "nonce-7f31ac0e59"
	notice := "I couldn't automatically match this to a Narvi account. Connect it here: https://narvi.example/auth/identity-link/" + nonce

	if err := d.CreateThoughtActivity(context.Background(), "tok", "agent-session-1", "Narvi has started working on this.", notice); err != nil {
		t.Fatalf("CreateThoughtActivity: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(store.rows))
	}
	spec := string(store.rows[0].SpecJson)
	if strings.Contains(spec, nonce) {
		t.Errorf("the magic-link nonce reached spec_json: %s", spec)
	}
	if strings.Contains(spec, "identity-link") || strings.Contains(spec, "Connect it here") {
		t.Errorf("the identity-link prompt's text reached spec_json: %s", spec)
	}
	if !strings.Contains(spec, "identityNoticeAppended\":true") {
		t.Errorf("spec_json = %s, want it to record THAT a prompt was appended -- the fact is what the evaluator needs", spec)
	}
	if !strings.Contains(spec, "Narvi has started working on this.") {
		t.Errorf("spec_json = %s, want the activity's own body still recorded", spec)
	}
}
