//go:build integration

// Integration test for Step 67's ("review digest: description adequacy +
// graduated remediation", §26.2) own description-autofix notifier
// (descriptionautofix.go), against a real Postgres instance -- gated
// behind the "integration" build tag, reusing this package's own
// newTestPool helper (builder_integration_test.go).
package outboxworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeDescriptionAutofixSourceControl is a minimal test-only
// ports.SourceControl -- narrowed to exactly the two methods
// descriptionAutofixNotifier's own Deliver calls (GetPRBody, UpdatePRBody)
// -- every other method returns a clear "not implemented" error,
// mirroring fakeSentinelAutoFixSourceControl's own identical precedent
// (sentinelautofix_integration_test.go).
type fakeDescriptionAutofixSourceControl struct {
	mu sync.Mutex

	getPRBodyCalls int
	nextBody       string
	nextFound      bool
	nextGetErr     error

	updateCalls   []ports.UpdatePRBodySpec
	nextUpdateErr error
}

var _ ports.SourceControl = (*fakeDescriptionAutofixSourceControl)(nil)

func (f *fakeDescriptionAutofixSourceControl) GetPRBody(context.Context, string, string, int, string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getPRBodyCalls++
	if f.nextGetErr != nil {
		return "", false, f.nextGetErr
	}
	return f.nextBody, f.nextFound, nil
}

func (f *fakeDescriptionAutofixSourceControl) getPRBodyCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getPRBodyCalls
}

func (f *fakeDescriptionAutofixSourceControl) UpdatePRBody(_ context.Context, spec ports.UpdatePRBodySpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls = append(f.updateCalls, spec)
	return f.nextUpdateErr
}

func (f *fakeDescriptionAutofixSourceControl) updateCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updateCalls)
}

func (f *fakeDescriptionAutofixSourceControl) lastUpdateSpec() ports.UpdatePRBodySpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updateCalls[len(f.updateCalls)-1]
}

func (f *fakeDescriptionAutofixSourceControl) CreatePR(context.Context, ports.CreatePRSpec) (ports.PRRef, error) {
	return ports.PRRef{}, errors.New("fakeDescriptionAutofixSourceControl: CreatePR not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) ResolveBranchSHA(context.Context, ports.ResolveBranchSHASpec) (string, string, error) {
	return "", "", errors.New("fakeDescriptionAutofixSourceControl: ResolveBranchSHA not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) ResolveContractsFingerprint(context.Context, ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	return "", false, errors.New("fakeDescriptionAutofixSourceControl: ResolveContractsFingerprint not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) CheckRepoAccess(context.Context, ports.CheckRepoAccessSpec) (bool, error) {
	return false, errors.New("fakeDescriptionAutofixSourceControl: CheckRepoAccess not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) GetFileContent(context.Context, ports.GetFileContentSpec) (string, string, bool, error) {
	return "", "", false, errors.New("fakeDescriptionAutofixSourceControl: GetFileContent not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) UpdateFileContent(context.Context, ports.UpdateFileContentSpec) (string, error) {
	return "", errors.New("fakeDescriptionAutofixSourceControl: UpdateFileContent not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) RegisterPRStack(context.Context, ports.RegisterPRStackSpec) error {
	return errors.New("fakeDescriptionAutofixSourceControl: RegisterPRStack not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, bool, error) {
	return nil, false, errors.New("fakeDescriptionAutofixSourceControl: ListMergedBetween not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) CreateBranch(context.Context, ports.CreateBranchSpec) error {
	return errors.New("fakeDescriptionAutofixSourceControl: CreateBranch not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) GetOpenPR(context.Context, string, string, int, string) (ports.OpenPR, bool, error) {
	return ports.OpenPR{}, false, errors.New("fakeDescriptionAutofixSourceControl: GetOpenPR not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) ListOpenPRsForUser(context.Context, ports.ListOpenPRsForUserSpec) ([]ports.OpenPR, bool, error) {
	return nil, false, errors.New("fakeDescriptionAutofixSourceControl: ListOpenPRsForUser not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) ResolveCodeOwners(context.Context, ports.ResolveCodeOwnersSpec) ([]ports.Owner, error) {
	return nil, errors.New("fakeDescriptionAutofixSourceControl: ResolveCodeOwners not implemented")
}
func (f *fakeDescriptionAutofixSourceControl) MergePR(context.Context, ports.MergePRSpec) (string, error) {
	return "", errors.New("fakeDescriptionAutofixSourceControl: MergePR not implemented")
}

// seedPlatformAuthoredPR creates a real session and a real "pr"-typed
// artifact row for it, at the deterministic
// "https://github.com/{owner}/{repo}/pull/{number}" URL -- mirrors
// internal/app/automerge's own seedEligiblePR precedent (worker_
// integration_test.go) for the identical "make isPlatformAuthored find a
// real row" need.
func seedPlatformAuthoredPR(ctx context.Context, t *testing.T, pool *pgxpool.Pool, owner, repo string, number int) {
	t.Helper()

	sessions := narvipg.NewSessionStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create platform session: %v", err)
	}

	htmlURL := "https://github.com/" + owner + "/" + repo + "/pull/" + strconv.Itoa(number)
	if _, err := artifacts.Create(ctx, sqlcgen.CreateArtifactParams{
		SessionID: session.ID, Type: sqlcgen.ArtifactTypePr, Url: htmlURL, Metadata: []byte("{}"),
	}); err != nil {
		t.Fatalf("create pr artifact: %v", err)
	}
}

func descriptionAutofixPayload(t *testing.T, owner, repo string, number int, proposedBody string) []byte {
	t.Helper()
	payload, err := json.Marshal(ports.DescriptionAutofixPayload{
		Owner:        owner,
		Repo:         repo,
		PRNumber:     number,
		ProposedBody: proposedBody,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return payload
}

// TestDescriptionAutofixNotifier_FlagOff_NeverWrites is this Step's own
// central pin: "the server-side flag check refusing when the flag is
// off". No repo_settings row at all (the table's own established
// fail-closed-on-missing-row precedent) means the flag defaults to OFF --
// Deliver must return nil (a confirmed, non-retried no-op) and NEVER call
// GetPRBody/UpdatePRBody, even though the PR IS genuinely platform-
// authored.
func TestDescriptionAutofixNotifier_FlagOff_NeverWrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "flagoff-repo", 101
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)
	// Deliberately NO repo_settings row for this repo at all.

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: true, nextBody: "Original body."}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil (a confirmed flag-off is a silent no-op, never an error)", err)
	}

	if sourceControl.getPRBodyCallCount() != 0 {
		t.Errorf("GetPRBody called %d times, want 0 (flag off must short-circuit before any GitHub call)", sourceControl.getPRBodyCallCount())
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0", sourceControl.updateCallCount())
	}
}

// TestDescriptionAutofixNotifier_FlagExplicitlyOff_NeverWrites is the
// sibling of the test above with a REAL repo_settings row present, flag
// explicitly false (as opposed to the row being entirely absent) -- both
// must degrade identically.
func TestDescriptionAutofixNotifier_FlagExplicitlyOff_NeverWrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "flagexplicitoff-repo", 102
	repoFullName := owner + "/" + repo
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)
	if _, err := repoSettings.UpsertDescriptionAutofixToggle(ctx, repoFullName, false); err != nil {
		t.Fatalf("upsert repo settings (flag off): %v", err)
	}

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: true, nextBody: "Original body."}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0 (flag explicitly off)", sourceControl.updateCallCount())
	}
}

// TestDescriptionAutofixNotifier_NotPlatformAuthored_NeverWrites is this
// Step's own other central pin: "the server-side authorship check
// refusing a human-authored PR". The flag is ON, but no artifacts row
// exists for this PR's own URL (a human-authored PR) -- Deliver must
// return nil and never call UpdatePRBody, even though the flag is armed.
func TestDescriptionAutofixNotifier_NotPlatformAuthored_NeverWrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "notauthored-repo", 103
	repoFullName := owner + "/" + repo
	if _, err := repoSettings.UpsertDescriptionAutofixToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("upsert repo settings (flag on): %v", err)
	}
	// Deliberately NO artifact row -- this PR was opened by a human, not
	// a Narvi session.

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: true, nextBody: "Original body."}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil (a confirmed non-platform-authored PR is a silent no-op, never an error)", err)
	}

	if sourceControl.getPRBodyCallCount() != 0 {
		t.Errorf("GetPRBody called %d times, want 0 (a human-authored PR must never reach the GitHub fetch step)", sourceControl.getPRBodyCallCount())
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0", sourceControl.updateCallCount())
	}
}

// TestDescriptionAutofixNotifier_FlagOnAndPlatformAuthored_WritesComposedBody
// is the happy path both checks passing: the PR's own body is rewritten
// via UpdatePRBody, with the freshly-fetched ORIGINAL body preserved
// alongside the proposed one (internal/domain/reviewpost.RenderAutofixBody).
func TestDescriptionAutofixNotifier_FlagOnAndPlatformAuthored_WritesComposedBody(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "eligible-repo", 104
	repoFullName := owner + "/" + repo
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)
	if _, err := repoSettings.UpsertDescriptionAutofixToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("upsert repo settings (flag on): %v", err)
	}

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: true, nextBody: "The CURRENT live body, freshly fetched."}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	proposedBody := "This PR rewrites the auth token refresh path to retry on transient failures."
	payload := descriptionAutofixPayload(t, owner, repo, number, proposedBody)
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	if sourceControl.getPRBodyCallCount() != 1 {
		t.Fatalf("GetPRBody called %d times, want 1", sourceControl.getPRBodyCallCount())
	}
	if sourceControl.updateCallCount() != 1 {
		t.Fatalf("UpdatePRBody called %d times, want 1", sourceControl.updateCallCount())
	}

	spec := sourceControl.lastUpdateSpec()
	if spec.Owner != owner || spec.Repo != repo || spec.Number != number {
		t.Errorf("UpdatePRBodySpec owner/repo/number = %s/%s/%d, want %s/%s/%d", spec.Owner, spec.Repo, spec.Number, owner, repo, number)
	}
	if spec.Token != "gh-fake-bot-token" {
		t.Errorf("UpdatePRBodySpec.Token = %q, want the bot token (system-initiated, no human creator to attribute to)", spec.Token)
	}
	if !strings.Contains(spec.Body, proposedBody) {
		t.Errorf("UpdatePRBodySpec.Body missing the proposed text, got:\n%s", spec.Body)
	}
	if !strings.Contains(spec.Body, "The CURRENT live body, freshly fetched.") {
		t.Errorf("UpdatePRBodySpec.Body missing the freshly-fetched ORIGINAL body -- must be preserved in a collapsed block, got:\n%s", spec.Body)
	}
}

// TestDescriptionAutofixNotifier_PRNoLongerFound_NeverWrites proves a PR
// that has since been closed/deleted/made unreachable (GetPRBody's own
// found=false, err=nil) is a legitimate, silent no-op, never an error.
func TestDescriptionAutofixNotifier_PRNoLongerFound_NeverWrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "gone-repo", 105
	repoFullName := owner + "/" + repo
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)
	if _, err := repoSettings.UpsertDescriptionAutofixToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("upsert repo settings (flag on): %v", err)
	}

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: false}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0 (the PR was not found)", sourceControl.updateCallCount())
	}
}

// TestDescriptionAutofixNotifier_GetPRBodyFails_ReturnsErrorForRetry
// proves a genuine, uncertain GitHub API failure fetching the current
// body propagates as a real error (so the outbox retries later) rather
// than degrading to a silent no-op the way a CONFIRMED-negative check
// does.
func TestDescriptionAutofixNotifier_GetPRBodyFails_ReturnsErrorForRetry(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "geterr-repo", 106
	repoFullName := owner + "/" + repo
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)
	if _, err := repoSettings.UpsertDescriptionAutofixToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("upsert repo settings (flag on): %v", err)
	}

	sourceControl := &fakeDescriptionAutofixSourceControl{nextGetErr: errors.New("simulated transient GitHub API failure")}
	notifier := outboxworker.NewDescriptionAutofixNotifier(repoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err == nil {
		t.Fatal("Deliver() error = nil, want a real error when GetPRBody itself fails (a genuine, uncertain failure must be retried, never silently swallowed)")
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0 (never reached after GetPRBody fails)", sourceControl.updateCallCount())
	}
}

// TestDescriptionAutofixNotifier_RepoSettingsReadError_ReturnsErrorForRetry
// proves a genuine, uncertain Postgres read error checking the repo's own
// flag ALSO propagates as a real error, never silently treated as though
// the flag were confirmed off -- fault-injected via an already-rolled-
// back tx, mirroring internal/app/decisioninbox's own established
// TestBuild_CredentialResolutionErrorDegradesRatherThanRenderingNoGitHub
// precedent (aggregate_integration_test.go) for the identical technique.
func TestDescriptionAutofixNotifier_RepoSettingsReadError_ReturnsErrorForRetry(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	artifacts := narvipg.NewArtifactStore(pool)

	owner, repo, number := "acme", "repoerr-repo", 107
	seedPlatformAuthoredPR(ctx, t, pool, owner, repo, number)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback tx: %v", err)
	}
	brokenRepoSettings := narvipg.NewRepoSettingsStore(pool).WithTx(tx)

	sourceControl := &fakeDescriptionAutofixSourceControl{nextFound: true, nextBody: "Original body."}
	notifier := outboxworker.NewDescriptionAutofixNotifier(brokenRepoSettings, artifacts, sourceControl, "gh-fake-bot-token", platform.DefaultTimeouts())

	payload := descriptionAutofixPayload(t, owner, repo, number, "Proposed new body.")
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindGitHubDescriptionAutofix, Payload: payload}); err == nil {
		t.Fatal("Deliver() error = nil, want a real error when the repo_settings read itself genuinely fails")
	}
	if sourceControl.updateCallCount() != 0 {
		t.Errorf("UpdatePRBody called %d times, want 0", sourceControl.updateCallCount())
	}
}
