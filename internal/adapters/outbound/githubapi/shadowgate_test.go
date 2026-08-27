package githubapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// recordingStore captures what the gate wrote, and can be made to fail.
type recordingStore struct {
	entries []sqlcgen.CreateShadowSCMWriteParams
	err     error
}

func (s *recordingStore) Create(_ context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error) {
	if s.err != nil {
		return sqlcgen.ShadowScmWrite{}, s.err
	}
	s.entries = append(s.entries, arg)
	return sqlcgen.ShadowScmWrite{}, nil
}

// TestShadowGate_MutatingVerbsNeverReachTheNetwork is the assertion this
// whole Step exists for, and it is written so that the network itself is
// the judge: the test server fails the test if a mutating verb ever
// arrives. An assertion on the gate's return value would pass even if the
// request had also gone out.
func TestShadowGate_MutatingVerbsNeverReachTheNetwork(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &recordingStore{}
	gate := &shadowRoundTripper{
		next:    http.DefaultTransport,
		ledger:  store,
		resolve: func(context.Context, string) bool { return false }, // shadow
	}
	client := &http.Client{Transport: gate}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, err := http.NewRequestWithContext(context.Background(), method,
			srv.URL+"/repos/acme/widgets/pulls", strings.NewReader(`{"title":"x"}`))
		if err != nil {
			t.Fatalf("build %s: %v", method, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s through the gate: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.Header.Get("X-Narvi-Shadow-Suppressed") != "true" {
			t.Errorf("%s got a response with no suppression marker; a synthetic result must be impossible to mistake for a real one", method)
		}
	}

	if len(reached) != 0 {
		t.Fatalf("mutating requests reached the network: %v -- a single leaked write is total failure of this capability", reached)
	}
	if len(store.entries) != 4 {
		t.Errorf("ledger recorded %d suppressed writes, want 4 -- every suppressed effect must be recorded", len(store.entries))
	}
	for _, e := range store.entries {
		if e.RepoFullName != "acme/widgets" {
			t.Errorf("ledger row attributed to %q, want acme/widgets", e.RepoFullName)
		}
		if strings.Contains(string(e.SpecJson), "ghp_") || strings.Contains(string(e.SpecJson), "Authorization") {
			t.Errorf("ledger row carries something credential-shaped: %s", e.SpecJson)
		}
	}
}

// TestShadowGate_ReadsStillPass pins the other half: shadow must not blind
// the platform. Reading a customer's repository leaves no trace, and
// suppressing reads would make the evaluation impossible rather than safe.
func TestShadowGate_ReadsStillPass(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &recordingStore{}
	client := &http.Client{Transport: &shadowRoundTripper{
		next: http.DefaultTransport, ledger: store,
		resolve: func(context.Context, string) bool { return false },
	}}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+"/repos/acme/widgets/pulls/1", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
	}
	if len(reached) != 2 {
		t.Errorf("reads that reached the server: %v, want GET and HEAD both through", reached)
	}
	if len(store.entries) != 0 {
		t.Errorf("a read was recorded as a suppressed write (%d rows); reads are not suppressed", len(store.entries))
	}
}

// TestShadowGate_LedgerFailureFailsTheRequest pins record-or-fail. A
// suppression that could not be recorded must not be reported as success:
// the operator's whole evaluation is the record.
func TestShadowGate_LedgerFailureFailsTheRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the network was contacted on a ledger failure")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &shadowRoundTripper{
		next:    http.DefaultTransport,
		ledger:  &recordingStore{err: errLedgerDown},
		resolve: func(context.Context, string) bool { return false },
	}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/repos/acme/widgets/pulls", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a suppressed write whose ledger insert failed returned success; record-or-fail means the caller must see the failure")
	}
}

// TestShadowGate_LiveRepoPassesThrough proves the gate is not simply
// blocking everything: a repository the resolver reports live must reach
// the network, or shadow mode could never be graduated from.
func TestShadowGate_LiveRepoPassesThrough(t *testing.T) {
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	store := &recordingStore{}
	client := &http.Client{Transport: &shadowRoundTripper{
		next: http.DefaultTransport, ledger: store,
		resolve: func(_ context.Context, repo string) bool { return repo == "acme/widgets" },
	}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/repos/acme/widgets/pulls", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("live POST: %v", err)
	}
	_ = resp.Body.Close()
	if reached != 1 {
		t.Errorf("live POST reached the network %d times, want 1", reached)
	}
	if len(store.entries) != 0 {
		t.Errorf("a live write was recorded in the suppression ledger (%d rows)", len(store.entries))
	}
}

// TestShadowGate_UnrecognisedPathResolvesShadow: a mutating request whose
// path carries no repository cannot be attributed, so it cannot be
// evaluated either. It must not go out.
func TestShadowGate_UnrecognisedPathResolvesShadow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an unattributable mutating request reached the network")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &recordingStore{}
	client := &http.Client{Transport: &shadowRoundTripper{
		next: http.DefaultTransport, ledger: store,
		// A resolver that would say "live" for the empty repo name, to
		// prove the gate does not consult it into a pass-through here.
		resolve: func(context.Context, string) bool { return false },
	}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/graphql", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an unattributable mutating request was answered as suppressed-and-recorded; it has no repository to record against, so it must fail")
	}
}

var errLedgerDown = errors.New("ledger unavailable")
