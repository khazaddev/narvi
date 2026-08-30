package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"

	"github.com/khazaddev/narvi/internal/domain/shadowsentinel"
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
		// A resolver that says LIVE for everything, including the empty
		// repository name. If the gate consulted it here, this request
		// would go out -- which is exactly what the previous version of
		// this test could not detect, because it passed a resolver that
		// said shadow and would have passed either way.
		resolve: func(context.Context, string) bool { return true },
	}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/graphql", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an unattributable mutating request was answered as suppressed-and-recorded; it has no repository to record against, so it must fail")
	}
}

var errLedgerDown = errors.New("ledger unavailable")

// TestNew_WithoutATransportCannotReachAnything pins §30.2's "the zero
// value fails closed". This constructor used to default a nil client to
// http.DefaultClient, which made New(nil, base) a WORKING, gate-free
// adapter that no layer above could see -- the attractive nuisance the
// section names. A nil client now yields one that can make no request at
// all, so forgetting the gate is useless rather than dangerous.
func TestNew_WithoutATransportCannotReachAnything(t *testing.T) {
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(nil, srv.URL)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/repos/acme/widgets", nil)
	if _, err := a.httpClient.Do(req); err == nil {
		t.Fatal("an adapter built with no transport reached the network; the omission must fail closed")
	}
	if reached != 0 {
		t.Fatalf("the server was contacted %d times by a gate-less adapter", reached)
	}
}

// TestSynthesizedResponse_CarriesTheSameSentinelsTheDecoratorReturns
// closes the gap between the two layers that can each suppress.
//
// §30.2 puts this transport gate underneath the typed port decorator as a
// fallback net, and each resolves the egress flag for itself. When they
// disagree — one transient repo_settings read failure is enough, since
// that read fails closed — the decorator calls the live client and gets
// this response. A body with no fields parsed into PRRef{Number: 0,
// URL: ""} and an empty commit SHA: zero values no synthetic-value check
// recognises, so downstream lanes ran against a pull request that does
// not exist and nothing failed to say so.
func TestSynthesizedResponse_CarriesTheSameSentinelsTheDecoratorReturns(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/repos/acme/widgets/pulls", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp := synthesizedResponse(req)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var parsed struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		SHA     string `json:"sha"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal synthesized body: %v", err)
	}

	if parsed.Number != shadowsentinel.PRNumber {
		t.Errorf("number = %d, want the synthetic sentinel %d: a caller parsing this must be able to tell it from a real PR", parsed.Number, shadowsentinel.PRNumber)
	}
	if !strings.HasPrefix(parsed.HTMLURL, shadowsentinel.URLScheme) {
		t.Errorf("html_url = %q, want the %q scheme so it cannot be followed or matched as a real GitHub URL", parsed.HTMLURL, shadowsentinel.URLScheme)
	}
	if parsed.SHA != shadowsentinel.CommitSHA {
		t.Errorf("sha = %q, want the synthetic sentinel %q", parsed.SHA, shadowsentinel.CommitSHA)
	}
}
