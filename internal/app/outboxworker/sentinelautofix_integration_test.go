//go:build integration

// Integration test for Step 48's ("sentinels + suggestions", §17.2) own
// sentinel-auto-fix notifier (sentinelautofix.go), against a real Postgres
// instance -- gated behind the "integration" build tag, reusing this
// package's own newTestPool helper (builder_integration_test.go).
package outboxworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeSentinelAutoFixSourceControl is a minimal test-only ports.
// SourceControl -- narrowed to exactly the two methods Deliver's own
// createFixBranch calls (ResolveBranchSHA, CreateBranch, confirmed-finding
// fix) -- every other method returns a clear "not implemented" error,
// mirroring internal/app/sessionactor's own fakeSourceControl precedent.
type fakeSentinelAutoFixSourceControl struct {
	mu sync.Mutex

	shaCalls   []ports.ResolveBranchSHASpec
	nextSHA    string
	nextSHAErr error

	createBranchCalls []ports.CreateBranchSpec
	createBranchErr   error
}

var _ ports.SourceControl = (*fakeSentinelAutoFixSourceControl)(nil)

func (f *fakeSentinelAutoFixSourceControl) CreatePR(context.Context, ports.CreatePRSpec) (ports.PRRef, error) {
	return ports.PRRef{}, errors.New("fakeSentinelAutoFixSourceControl: CreatePR not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) ResolveBranchSHA(_ context.Context, spec ports.ResolveBranchSHASpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shaCalls = append(f.shaCalls, spec)
	if f.nextSHAErr != nil {
		return "", "", f.nextSHAErr
	}
	return f.nextSHA, spec.Branch, nil
}

func (f *fakeSentinelAutoFixSourceControl) shaCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.shaCalls)
}

func (f *fakeSentinelAutoFixSourceControl) lastSHASpec() ports.ResolveBranchSHASpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shaCalls[len(f.shaCalls)-1]
}

func (f *fakeSentinelAutoFixSourceControl) ResolveContractsFingerprint(context.Context, ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	return "", false, errors.New("fakeSentinelAutoFixSourceControl: ResolveContractsFingerprint not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) CheckRepoAccess(context.Context, ports.CheckRepoAccessSpec) (bool, error) {
	return false, errors.New("fakeSentinelAutoFixSourceControl: CheckRepoAccess not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) GetFileContent(context.Context, ports.GetFileContentSpec) (string, string, bool, error) {
	return "", "", false, errors.New("fakeSentinelAutoFixSourceControl: GetFileContent not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) UpdateFileContent(context.Context, ports.UpdateFileContentSpec) (string, error) {
	return "", errors.New("fakeSentinelAutoFixSourceControl: UpdateFileContent not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) RegisterPRStack(context.Context, ports.RegisterPRStackSpec) error {
	return errors.New("fakeSentinelAutoFixSourceControl: RegisterPRStack not implemented")
}

// ListMergedBetween (Step 50, "release PR review", §15.2) is never
// reached from this package -- same "not implemented" precedent as
// RegisterPRStack above.
func (f *fakeSentinelAutoFixSourceControl) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, bool, error) {
	return nil, false, errors.New("fakeSentinelAutoFixSourceControl: ListMergedBetween not implemented")
}

func (f *fakeSentinelAutoFixSourceControl) CreateBranch(_ context.Context, spec ports.CreateBranchSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createBranchCalls = append(f.createBranchCalls, spec)
	return f.createBranchErr
}

func (f *fakeSentinelAutoFixSourceControl) createBranchCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.createBranchCalls)
}

func (f *fakeSentinelAutoFixSourceControl) lastCreateBranchSpec() ports.CreateBranchSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createBranchCalls[len(f.createBranchCalls)-1]
}

// TestSentinelAutoFixNotifier_SpawnsChildSessionAndUpdatesStores proves
// the notifier's own real Deliver: a real child session is spawned
// (httpapi.SpawnChildSession), sentinel_fixes.fix_child_session_id is
// recorded, and every named finding moves to 'fix_pending'.
func TestSentinelAutoFixNotifier_SpawnsChildSessionAndUpdatesStores(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	reviewFindings := narvipg.NewReviewFindingStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	originSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}

	repoFullName := "acme/notifier-test-repo"
	fix, err := sentinelFixes.Claim(ctx, repoFullName, 77, originSession.ID, "feature-fix-me")
	if err != nil {
		t.Fatalf("claim sentinel_fixes: %v", err)
	}

	const identityHash = "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123ab"
	if _, err := reviewFindings.Upsert(ctx, sqlcgen.UpsertReviewFindingParams{
		RepoFullName: repoFullName,
		PrNumber:     77,
		IdentityHash: identityHash,
		Severity:     "medium",
		FilePath:     "internal/foo/bar.go",
		Description:  "Missing test coverage.",
	}); err != nil {
		t.Fatalf("upsert review finding: %v", err)
	}

	sourceControl := &fakeSentinelAutoFixSourceControl{nextSHA: "deadbeef"}
	notifier := outboxworker.NewSentinelAutoFixNotifier(pool, sessions, turns, environments, auditLog, registry, sentinelFixes, reviewFindings,
		sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload, err := json.Marshal(ports.SentinelAutoFixPayload{
		SentinelFixID:         fix.ID.String(),
		RepoFullName:          repoFullName,
		OriginPRNumber:        77,
		OriginReviewSessionID: originSession.ID.String(),
		OriginHeadBranch:      "feature-fix-me",
		RepoName:              "widgets",
		RepoCloneURL:          "https://github.com/acme/widgets.git",
		FindingIdentityHashes: []string{identityHash},
		FindingDescriptions:   []string{"Missing test coverage."},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	updatedFix, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if !updatedFix.FixChildSessionID.Valid {
		t.Fatal("FixChildSessionID is still invalid after Deliver, want a real child session id")
	}
	if updatedFix.Status != "spawned" {
		t.Errorf("Status = %q, want %q", updatedFix.Status, "spawned")
	}

	childSession, err := sessions.Get(ctx, updatedFix.FixChildSessionID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if childSession.ProvenanceTag == nil || *childSession.ProvenanceTag != "sentinel_auto_fix" {
		t.Errorf("child session ProvenanceTag = %v, want %q", childSession.ProvenanceTag, "sentinel_auto_fix")
	}
	if childSession.SpawnDepth != 1 {
		t.Errorf("child session SpawnDepth = %d, want 1", childSession.SpawnDepth)
	}
	if !childSession.ParentSessionID.Valid || childSession.ParentSessionID != originSession.ID {
		t.Errorf("child session ParentSessionID = %v, want %v", childSession.ParentSessionID, originSession.ID)
	}

	// Confirmed-finding fix: the child session's own repos[0].branch must
	// be a BRAND-NEW, distinct branch -- NEVER the origin PR's own literal
	// head branch ("feature-fix-me") -- since a session's repos[].branch
	// is what both the boot-time clone/checkout AND the eventual push
	// target. Before this fix, this was "feature-fix-me" verbatim, which
	// would have checked out and pushed back to the SAME branch as the
	// still-open origin PR.
	var childRepos []struct {
		Name   string  `json:"name"`
		Url    string  `json:"url"`
		Branch *string `json:"branch"`
	}
	if err := json.Unmarshal(childSession.Repos, &childRepos); err != nil {
		t.Fatalf("unmarshal child session repos: %v", err)
	}
	if len(childRepos) != 1 {
		t.Fatalf("child session repos = %d, want 1", len(childRepos))
	}
	if childRepos[0].Branch == nil {
		t.Fatal("child session repos[0].branch is nil, want a real, distinct branch name")
	}
	gotChildBranch := *childRepos[0].Branch
	if gotChildBranch == "feature-fix-me" {
		t.Errorf("child session repos[0].branch = %q, want it DISTINCT from the origin PR's own head branch %q -- checking out/pushing the SAME branch as the origin silently fast-forwards the still-open origin PR with an unreviewed commit, and dooms the eventual fix-PR CreatePR call to Head == Base",
			gotChildBranch, "feature-fix-me")
	}
	if !strings.Contains(gotChildBranch, fix.ID.String()) {
		t.Errorf("child session repos[0].branch = %q, want it to reference the sentinel_fixes claim id %q so it is stable/deterministic across redeliveries", gotChildBranch, fix.ID.String())
	}

	// The new branch must be created FROM the origin head branch's own
	// current tip -- ResolveBranchSHA called with Branch: "feature-fix-me"
	// (never a guess), and CreateBranch called with that exact resolved
	// SHA and the SAME branch name just asserted above.
	if sourceControl.shaCallCount() != 1 {
		t.Fatalf("ResolveBranchSHA called %d times, want 1", sourceControl.shaCallCount())
	}
	shaSpec := sourceControl.lastSHASpec()
	if shaSpec.Owner != "acme" || shaSpec.Repo != "widgets" || shaSpec.Branch != "feature-fix-me" {
		t.Errorf("ResolveBranchSHASpec = %+v, want Owner=acme Repo=widgets Branch=feature-fix-me", shaSpec)
	}
	if sourceControl.createBranchCallCount() != 1 {
		t.Fatalf("CreateBranch called %d times, want 1", sourceControl.createBranchCallCount())
	}
	createSpec := sourceControl.lastCreateBranchSpec()
	if createSpec.Branch != gotChildBranch {
		t.Errorf("CreateBranchSpec.Branch = %q, want it to match the child session's own repos[0].branch %q", createSpec.Branch, gotChildBranch)
	}
	if createSpec.SHA != "deadbeef" {
		t.Errorf("CreateBranchSpec.SHA = %q, want %q (the origin branch's own resolved current tip)", createSpec.SHA, "deadbeef")
	}

	finding, err := reviewFindings.Get(ctx, repoFullName, 77, identityHash)
	if err != nil {
		t.Fatalf("get review finding: %v", err)
	}
	if finding.Status != "fix_pending" {
		t.Errorf("finding Status = %q, want %q", finding.Status, "fix_pending")
	}
	if !finding.FixChildSessionID.Valid || finding.FixChildSessionID != updatedFix.FixChildSessionID {
		t.Errorf("finding FixChildSessionID = %v, want %v", finding.FixChildSessionID, updatedFix.FixChildSessionID)
	}

	// Idempotency: a redelivered/retried outbox entry must never spawn a
	// SECOND child session for the SAME claim.
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err != nil {
		t.Fatalf("second Deliver() error = %v", err)
	}
	refetched, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes after second deliver: %v", err)
	}
	if refetched.FixChildSessionID != updatedFix.FixChildSessionID {
		t.Errorf("FixChildSessionID changed after a redelivered outbox entry: %v -> %v, want it unchanged (idempotent, never a second spawn)",
			updatedFix.FixChildSessionID, refetched.FixChildSessionID)
	}
	// The idempotency short-circuit (fix.FixChildSessionID.Valid) fires
	// BEFORE createFixBranch is ever called again -- a redelivery must
	// never re-resolve/re-create the branch either.
	if got := sourceControl.shaCallCount(); got != 1 {
		t.Errorf("ResolveBranchSHA called %d times after a redelivered outbox entry, want still 1 (idempotency short-circuit fires before it)", got)
	}
	if got := sourceControl.createBranchCallCount(); got != 1 {
		t.Errorf("CreateBranch called %d times after a redelivered outbox entry, want still 1 (idempotency short-circuit fires before it)", got)
	}
}

// TestSentinelAutoFixNotifier_ResolveBranchSHAFails_NeverSpawnsChildSession
// proves the confirmed-finding fix's own error path: when the origin head
// branch's own current SHA cannot be resolved, Deliver returns a real
// error (so the outbox worker's own backoff/retry machinery retries
// later) and never spawns a child session with a wrong/fallback branch.
func TestSentinelAutoFixNotifier_ResolveBranchSHAFails_NeverSpawnsChildSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	reviewFindings := narvipg.NewReviewFindingStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	originSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}

	repoFullName := "acme/notifier-resolve-sha-fails-repo"
	fix, err := sentinelFixes.Claim(ctx, repoFullName, 88, originSession.ID, "feature-fix-me-2")
	if err != nil {
		t.Fatalf("claim sentinel_fixes: %v", err)
	}

	sourceControl := &fakeSentinelAutoFixSourceControl{nextSHAErr: errors.New("simulated GitHub API failure resolving origin head branch")}
	notifier := outboxworker.NewSentinelAutoFixNotifier(pool, sessions, turns, environments, auditLog, registry, sentinelFixes, reviewFindings,
		sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload, err := json.Marshal(ports.SentinelAutoFixPayload{
		SentinelFixID:         fix.ID.String(),
		RepoFullName:          repoFullName,
		OriginPRNumber:        88,
		OriginReviewSessionID: originSession.ID.String(),
		OriginHeadBranch:      "feature-fix-me-2",
		RepoName:              "widgets",
		RepoCloneURL:          "https://github.com/acme/widgets.git",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err == nil {
		t.Fatal("Deliver() error = nil, want a real error when the origin head branch's own SHA cannot be resolved")
	}

	if sourceControl.createBranchCallCount() != 0 {
		t.Errorf("CreateBranch called %d times, want 0 (never called when ResolveBranchSHA already failed)", sourceControl.createBranchCallCount())
	}

	refetched, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if refetched.FixChildSessionID.Valid {
		t.Error("FixChildSessionID is valid, want it to stay unset -- no child session should ever be spawned when the fix branch could not be created")
	}
}

// TestSentinelAutoFixNotifier_CreateBranchFails_NeverSpawnsChildSession is
// the sibling of the test above for CreateBranch's own failure: the SHA
// resolves fine, but creating the new branch ref itself fails.
func TestSentinelAutoFixNotifier_CreateBranchFails_NeverSpawnsChildSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	reviewFindings := narvipg.NewReviewFindingStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	originSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}

	repoFullName := "acme/notifier-create-branch-fails-repo"
	fix, err := sentinelFixes.Claim(ctx, repoFullName, 89, originSession.ID, "feature-fix-me-3")
	if err != nil {
		t.Fatalf("claim sentinel_fixes: %v", err)
	}

	sourceControl := &fakeSentinelAutoFixSourceControl{nextSHA: "deadbeef", createBranchErr: errors.New("simulated GitHub API failure creating branch")}
	notifier := outboxworker.NewSentinelAutoFixNotifier(pool, sessions, turns, environments, auditLog, registry, sentinelFixes, reviewFindings,
		sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload, err := json.Marshal(ports.SentinelAutoFixPayload{
		SentinelFixID:         fix.ID.String(),
		RepoFullName:          repoFullName,
		OriginPRNumber:        89,
		OriginReviewSessionID: originSession.ID.String(),
		OriginHeadBranch:      "feature-fix-me-3",
		RepoName:              "widgets",
		RepoCloneURL:          "https://github.com/acme/widgets.git",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err == nil {
		t.Fatal("Deliver() error = nil, want a real error when CreateBranch itself fails")
	}

	refetched, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if refetched.FixChildSessionID.Valid {
		t.Error("FixChildSessionID is valid, want it to stay unset -- no child session should ever be spawned when the fix branch could not be created")
	}
}
