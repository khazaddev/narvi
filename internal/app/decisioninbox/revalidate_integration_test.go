//go:build integration

// Integration tests for RevalidateForMerge (Step 60, "decision inbox:
// read model + API", §16.2's own Merge endpoint) against a REAL Postgres
// instance -- gated behind the "integration" build tag, same package
// (decisioninbox_test) and newTestPool/fakeDecisionInboxSourceControl
// precedent as aggregate_integration_test.go. Deliberately ONE shared
// pool across every subtest below (t.Run, not separate top-level Test
// functions) -- mirrors this package's own TestBuild_FullScenario, and
// this repo's own documented aversion to spinning up more testcontainers
// than necessary (Makefile's own top comment on test-integration).
package decisioninbox_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/reposource"
)

// revalidateStores bundles the Postgres stores RevalidateForMerge itself
// reads (SentinelFixes/ReviewFindings/Artifacts) plus a fake SourceControl
// (RevalidateForMerge's own direct parameter -- never deps.SCMCache, per
// that function's own doc comment) shared across every subtest below.
type revalidateStores struct {
	deps          decisioninbox.Deps
	sourceControl *fakeDecisionInboxSourceControl
}

func newRevalidateStores(pool *pgxpool.Pool) *revalidateStores {
	sc := &fakeDecisionInboxSourceControl{openPRsByExternalID: map[string][]ports.OpenPR{}}
	return &revalidateStores{
		sourceControl: sc,
		deps: decisioninbox.Deps{
			Plans:          narvipg.NewPlanStore(pool),
			Sessions:       narvipg.NewSessionStore(pool),
			Participants:   narvipg.NewParticipantStore(pool),
			Automations:    narvipg.NewAutomationStore(pool),
			Outbox:         narvipg.NewOutboxStore(pool),
			ReviewFindings: narvipg.NewReviewFindingStore(pool),
			SentinelFixes:  narvipg.NewSentinelFixStore(pool),
			Artifacts:      narvipg.NewArtifactStore(pool),
			Identities:     narvipg.NewIdentityStore(pool),
		},
	}
}

// eligiblePR seeds a FULLY ELIGIBLE target PR for (repoFullName, prNumber)
// under actorGitHubID -- platform-authored (an artifacts row is created),
// low-risk, CI green, no findings, no needs-human label, no changes
// requested, not a draft/handoff/sentinel-fix. Every test below starts
// from this exact baseline and perturbs ONE fact (§60 review finding T1:
// "the row renders ready_to_merge, then X changes -> assert the merge is
// refused"), proving each negative check independently actually gates
// the merge rather than merely existing in the source.
func (rs *revalidateStores) eligiblePR(ctx context.Context, t *testing.T, pool *pgxpool.Pool, actorGitHubID, repoFullName string, prNumber int) ports.OpenPR {
	t.Helper()

	owner, repo, ok := reposource.SplitFullName(repoFullName)
	if !ok {
		t.Fatalf("malformed repoFullName %q", repoFullName)
	}
	htmlURL := "https://github.com/" + repoFullName + "/pull/" + strconv.Itoa(prNumber)

	pr := ports.OpenPR{
		Owner: owner, Repo: repo, Number: prNumber,
		Title: "eligible pr", HTMLURL: htmlURL, HeadSHA: "sha-" + strconv.Itoa(prNumber),
		Assignees:    []ports.PRPerson{{ExternalID: actorGitHubID, Login: "actor"}},
		CIConclusion: ports.CIConclusionSuccess,
		Labels:       []string{"review:low-risk"},
	}
	rs.sourceControl.openPRsByExternalID[actorGitHubID] = append(rs.sourceControl.openPRsByExternalID[actorGitHubID], pr)

	// Platform-authored: a real artifacts row of type 'pr' at this exact
	// URL (isPlatformAuthored's own signal).
	session, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create platform session: %v", err)
	}
	if _, err := narvipg.NewArtifactStore(pool).Create(ctx, sqlcgen.CreateArtifactParams{
		SessionID: session.ID, Type: sqlcgen.ArtifactTypePr, Url: htmlURL, Metadata: []byte("{}"),
	}); err != nil {
		t.Fatalf("create pr artifact: %v", err)
	}

	return pr
}

// replaceTargetPR overwrites the ONE seeded OpenPR for actorGitHubID with
// a mutated copy -- the mechanism every negative subtest below uses to
// perturb exactly one fact off of the eligiblePR baseline.
func (rs *revalidateStores) replaceTargetPR(actorGitHubID string, pr ports.OpenPR) {
	rs.sourceControl.openPRsByExternalID[actorGitHubID] = []ports.OpenPR{pr}
}

// TestRevalidateForMerge_NegativeCases covers every fact RevalidateForMerge
// re-checks that used to have zero coverage (§60 review finding T1,
// CRITICAL): the CI-green re-check, the needs-human label, the §17
// sentinel-fix exclusion, draft, handoff, and (§60 review finding A4,
// folded in here since it is the SAME "the row renders ready_to_merge,
// then a fact changes" shape) HasChangesRequested. Each subtest starts
// from eligiblePR's own fully-eligible baseline and perturbs ONE fact.
func TestRevalidateForMerge_NegativeCases(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	rs := newRevalidateStores(pool)
	const actorGitHubID = "revalidate-actor-1"

	t.Run("EligibleBaseline_MergesOK", func(t *testing.T) {
		const repoFullName = "acme/revalidate-eligible"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 1)

		ok, headSHA, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if !ok {
			t.Fatalf("RevalidateForMerge() ok = false, reason = %q, want true (fixture is fully eligible)", reason)
		}
		if headSHA != pr.HeadSHA {
			t.Errorf("headSHA = %q, want %q", headSHA, pr.HeadSHA)
		}
	})

	t.Run("CIRed_Refused", func(t *testing.T) {
		// §60 review finding A2/T1: this is the one guard that directly
		// exercises the merge gate's own strict CI check -- deletable
		// before this test existed.
		const repoFullName = "acme/revalidate-ci-red"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 2)
		pr.CIConclusion = ports.CIConclusionFailure
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (CI is red)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	t.Run("NeedsHumanLabel_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-needs-human"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 3)
		pr.Labels = []string{"review:low-risk", "review:needs-human"}
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (review:needs-human label applied)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	t.Run("SentinelFixFollowUp_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-sentinel"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 4)

		originSession, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
		if err != nil {
			t.Fatalf("create origin session: %v", err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		fix, err := rs.deps.SentinelFixes.WithTx(tx).Claim(ctx, repoFullName, 999, originSession.ID, "origin-branch")
		if err != nil {
			t.Fatalf("claim sentinel fix: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit tx: %v", err)
		}
		// UpdateOpened(fix.ID, pr.Number) registers THIS pr.Number as the
		// fix PR itself -- §17's own structural exclusion, checked
		// against sentinel_fixes.fix_pr_number.
		if _, err := rs.deps.SentinelFixes.UpdateOpened(ctx, fix.ID, int32(pr.Number)); err != nil {
			t.Fatalf("update sentinel fix opened: %v", err)
		}

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (this PR is a registered sentinel-auto-fix follow-up)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	t.Run("Draft_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-draft"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 5)
		pr.Draft = true
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (this PR is a draft)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	// §60 review finding T6: the handoff-label branch, merge-path half
	// (aggregate.go's own read-path half lives in
	// aggregate_integration_test.go's TestBuild_HandoffPR).
	t.Run("HandoffLabel_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-handoff"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 6)
		pr.Labels = []string{"review:low-risk", "handoff"}
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (this PR carries the handoff label)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	// §60 review finding A4: HasChangesRequested is a hard merge blocker
	// -- the one review-decision fact this endpoint's own "approval
	// state" re-check actually gates on (never HasApprovingReview, which
	// is display-only -- see RevalidateForMerge's own doc comment).
	t.Run("HasChangesRequested_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-changes-requested"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 7)
		pr.HasChangesRequested = true
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (a reviewer requested changes)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	// §60 review finding T4: the needs-human exclusion's OTHER half --
	// paired with a low-risk label, exactly like aggregate.go's own
	// read-path equivalent fixture, proving needs-human wins regardless
	// of an otherwise-fully-eligible risk label.
	t.Run("NeedsHumanWithLowRisk_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-needs-human-low-risk"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 8)
		pr.Labels = []string{"review:low-risk", "review:needs-human"}
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, _, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (needs-human alongside low-risk must still refuse)")
		}
	})
}
