// This file verifies §30.3's family 1 claim: the synchronous GitHub
// ingress replies (internal/adapters/inbound/github/
// actornotauthorizedreply.go, planawaitingreply.go) need no refactor of
// their own, because they post through CommentPoster -- satisfied, in
// production, by the SAME *githubapi.Adapter instance every other write
// on this package rides -- and are therefore already covered by §30.2's
// layer 0 transport gate BY CONSTRUCTION. shadowgate_test.go already pins
// that gate's general behavior against a synthetic /repos/{owner}/{repo}/
// pulls path; this file pins the SAME gate against PostIssueComment's own
// real request shape specifically, so that claim is verified rather than
// assumed.
package githubapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPostIssueComment_ShadowGate_SuppressedAndRecorded proves a
// PostIssueComment call -- the exact call actornotauthorizedreply.go and
// planawaitingreply.go make -- never reaches the network in shadow, is
// recorded into the ledger attributed to the right repository, and
// carries neither the bot token nor an Authorization header into the
// record.
func TestPostIssueComment_ShadowGate_SuppressedAndRecorded(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &recordingStore{}
	gatedClient := NewGatedClient(store, func(context.Context, string) bool { return false })
	adapter := New(gatedClient, srv.URL)

	if err := adapter.PostIssueComment(context.Background(), "acme", "widgets", 42, "ghp_secret_bot_token", "please sign in to link your account"); err != nil {
		t.Fatalf("PostIssueComment() error = %v, want nil (the gate answers with a synthesized success)", err)
	}

	if len(reached) != 0 {
		t.Fatalf("PostIssueComment reached the network: %v -- a single leaked comment is total failure of this capability", reached)
	}
	if len(store.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(store.entries))
	}
	entry := store.entries[0]
	if entry.RepoFullName != "acme/widgets" {
		t.Errorf("RepoFullName = %q, want %q", entry.RepoFullName, "acme/widgets")
	}
	if !strings.Contains(string(entry.SpecJson), "please sign in to link your account") {
		t.Errorf("ledger row lost the comment body: %s", entry.SpecJson)
	}
	if strings.Contains(string(entry.SpecJson), "ghp_secret_bot_token") || strings.Contains(string(entry.SpecJson), "Authorization") {
		t.Errorf("the bot token reached the ledger: %s", entry.SpecJson)
	}
}

// TestPostIssueComment_ShadowGate_LiveRepoStillPostsForReal is the other
// half: a repository the resolver reports live must still receive the
// comment for real, or GitHub ingress replies could never work outside
// shadow mode either.
func TestPostIssueComment_ShadowGate_LiveRepoStillPostsForReal(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	store := &recordingStore{}
	gatedClient := NewGatedClient(store, func(context.Context, string) bool { return true })
	adapter := New(gatedClient, srv.URL)

	if err := adapter.PostIssueComment(context.Background(), "acme", "widgets", 42, "ghp_secret_bot_token", "a plan is awaiting approval"); err != nil {
		t.Fatalf("PostIssueComment() error = %v, want nil", err)
	}
	if !strings.Contains(gotBody, "a plan is awaiting approval") {
		t.Errorf("live request body = %q, want it to contain the posted comment", gotBody)
	}
	if len(store.entries) != 0 {
		t.Errorf("a live comment was recorded in the suppression ledger (%d rows)", len(store.entries))
	}
}
