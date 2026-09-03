package shadowscm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
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

// liveSpy fails the test if any write reaches it. Reads are allowed, and
// counted, because reads are supposed to pass through.
type liveSpy struct {
	ports.SourceControl
	t          *testing.T
	readCalls  int
	writeCalls int
}

func (l *liveSpy) CreatePR(context.Context, ports.CreatePRSpec) (ports.PRRef, error) {
	l.writeCalls++
	l.t.Error("CreatePR reached the live adapter in shadow")
	return ports.PRRef{}, nil
}
func (l *liveSpy) MergePR(context.Context, ports.MergePRSpec) (string, error) {
	l.writeCalls++
	l.t.Error("MergePR reached the live adapter in shadow")
	return "", nil
}
func (l *liveSpy) CreateBranch(context.Context, ports.CreateBranchSpec) error {
	l.writeCalls++
	l.t.Error("CreateBranch reached the live adapter in shadow")
	return nil
}
func (l *liveSpy) CheckRepoAccess(context.Context, ports.CheckRepoAccessSpec) (bool, error) {
	l.readCalls++
	return true, nil
}

func shadowDecorator(t *testing.T, store *fakeStore) (*Decorator, *liveSpy) {
	t.Helper()
	spy := &liveSpy{t: t}
	d, err := New(spy, store, func(context.Context, string) bool { return false })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, spy
}

// TestDecorator_MergePRReturnsTheSentinelAndNoSHA is the §30.7 case, and
// the one that must not be "fixed" into returning a plausible SHA. A
// fabricated merge commit becomes an audit row and a fake confirmation
// feeding the metric that justifies arming auto-merge for real.
func TestDecorator_MergePRReturnsTheSentinelAndNoSHA(t *testing.T) {
	store := &fakeStore{}
	d, _ := shadowDecorator(t, store)

	sha, err := d.MergePR(context.Background(), ports.MergePRSpec{
		Owner: "acme", Repo: "widgets", Number: 7, HeadSHA: "deadbeef", Token: "ghp_secret",
	})
	if !errors.Is(err, ports.ErrShadowSuppressed) {
		t.Fatalf("MergePR error = %v, want ports.ErrShadowSuppressed", err)
	}
	if sha != "" {
		t.Errorf("MergePR returned a merge SHA %q; a suppressed merge must invent nothing", sha)
	}
	if len(store.rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(store.rows))
	}
	if store.rows[0].ResultJson != nil {
		t.Errorf("the merge row carries a synthetic result %s; it must be NULL", store.rows[0].ResultJson)
	}
	if strings.Contains(string(store.rows[0].SpecJson), "ghp_secret") {
		t.Errorf("the token reached the ledger: %s", store.rows[0].SpecJson)
	}
}

// TestDecorator_SyntheticResultsAreUnmistakable pins §30.6's requirement
// that a synthetic result cannot be read as a real one. A plausible fake
// is the worst outcome: it survives into records and screens where nobody
// can tell it apart.
func TestDecorator_SyntheticResultsAreUnmistakable(t *testing.T) {
	store := &fakeStore{}
	d, _ := shadowDecorator(t, store)

	ref, err := d.CreatePR(context.Background(), ports.CreatePRSpec{
		Owner: "acme", Repo: "widgets", Head: "fix/x", Base: "main", Title: "t", Token: "ghp_secret",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if ref.Number > 0 {
		t.Errorf("synthetic PR number %d is in the range a real PR occupies", ref.Number)
	}
	if strings.HasPrefix(ref.URL, "https://") {
		t.Errorf("synthetic PR URL %q looks like a real link someone could click through", ref.URL)
	}
	sha, err := d.UpdateFileContent(context.Background(), ports.UpdateFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "a.txt", Token: "ghp_secret",
	})
	if err != nil {
		t.Fatalf("UpdateFileContent: %v", err)
	}
	if strings.Trim(sha, "0123456789abcdef") == "" {
		t.Errorf("synthetic commit SHA %q is valid hex and could name a real object", sha)
	}
	for _, row := range store.rows {
		if strings.Contains(string(row.SpecJson), "ghp_secret") {
			t.Errorf("the token reached the ledger: %s", row.SpecJson)
		}
	}
}

// TestDecorator_LedgerFailureFailsTheWrite pins record-or-fail at this
// layer too: a suppression that could not be recorded must not be reported
// as one.
func TestDecorator_LedgerFailureFailsTheWrite(t *testing.T) {
	d, _ := shadowDecorator(t, &fakeStore{err: errors.New("ledger down")})

	if err := d.CreateBranch(context.Background(), ports.CreateBranchSpec{
		Owner: "acme", Repo: "widgets", Branch: "fix/x", SHA: "abc",
	}); err == nil {
		t.Fatal("CreateBranch reported success when its ledger insert failed")
	}
}

// TestDecorator_ReadsAreForwarded: shadow must not blind the platform.
func TestDecorator_ReadsAreForwarded(t *testing.T) {
	store := &fakeStore{}
	d, spy := shadowDecorator(t, store)

	if _, err := d.CheckRepoAccess(context.Background(), ports.CheckRepoAccessSpec{}); err != nil {
		t.Fatalf("CheckRepoAccess: %v", err)
	}
	if spy.readCalls != 1 {
		t.Errorf("read forwarded %d times, want 1", spy.readCalls)
	}
	if len(store.rows) != 0 {
		t.Errorf("a read was recorded as a suppressed write (%d rows)", len(store.rows))
	}
}

// TestNew_RefusesAnIncompleteConstruction: there is no convenience default
// that yields a pass-through, because a pass-through obtained by omission
// is the failure this layer exists to remove.
func TestNew_RefusesAnIncompleteConstruction(t *testing.T) {
	live := &liveSpy{t: t}
	store := &fakeStore{}
	isLive := func(context.Context, string) bool { return true }

	if _, err := New(nil, store, isLive); err == nil {
		t.Error("New accepted a nil live adapter")
	}
	if _, err := New(live, nil, isLive); err == nil {
		t.Error("New accepted a nil ledger")
	}
	if _, err := New(live, store, nil); err == nil {
		t.Error("New accepted a nil resolver -- which would make every repo's mode undefined")
	}
}

// TestDecorator_SuppressCreatePR_IgnoresALiveCurrentMode is the promotion
// race §30.8 freezes the push/PR mode to prevent.
//
// The decorator is built here with isLive returning TRUE for everything --
// the repo has been promoted since the push went out. CreatePR would
// therefore open a real pull request on a branch that was only ever
// pushed under shadow. SuppressCreatePR must not consult that at all: the
// caller holds a decision already frozen, and the suppression must still
// be recorded.
func TestDecorator_SuppressCreatePR_IgnoresALiveCurrentMode(t *testing.T) {
	store := &fakeStore{}
	spy := &liveSpy{t: t}
	d, err := New(spy, store, func(context.Context, string) bool { return true })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ref, err := d.SuppressCreatePR(context.Background(), ports.CreatePRSpec{
		Owner: "acme", Repo: "widgets", Head: "feature-x", Base: "main",
		Title: "t", Body: "b", Token: "ghp_must_never_be_recorded",
	})
	if err != nil {
		t.Fatalf("SuppressCreatePR: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1: a suppressed effect that leaves no record is the §30.6 contract violation", len(store.rows))
	}
	if store.rows[0].Operation != "create_pr" {
		t.Errorf("Operation = %q, want %q", store.rows[0].Operation, "create_pr")
	}
	if strings.Contains(string(store.rows[0].SpecJson), "ghp_must_never_be_recorded") {
		t.Errorf("the token reached spec_json: %s", store.rows[0].SpecJson)
	}
	if ref.URL == "" || !strings.HasPrefix(ref.URL, "shadow-suppressed://") {
		t.Errorf("ref = %+v, want a synthetic ref the caller can tell apart from a real PR", ref)
	}
}

// TestDecorator_SuppressCreatePR_LedgerFailureFailsTheCall holds the
// frozen-decision entry point to the SAME record-or-fail rule as every
// other suppressed write: a suppression that cannot be evidenced must not
// report success to its caller.
func TestDecorator_SuppressCreatePR_LedgerFailureFailsTheCall(t *testing.T) {
	d, _ := shadowDecorator(t, &fakeStore{err: errors.New("ledger down")})

	if _, err := d.SuppressCreatePR(context.Background(), ports.CreatePRSpec{
		Owner: "acme", Repo: "widgets", Head: "feature-x", Base: "main",
	}); err == nil {
		t.Fatal("SuppressCreatePR reported success when its ledger insert failed")
	}
}
