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
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
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
			// Step 62 (§21.1/§21.2): the REAL auto-approval eligibility
			// engine's own store dependencies -- revalidateCore now reads
			// review_verdicts/repo_settings through this bundle.
			ReviewVerdict: appreviewverdict.Deps{
				ReviewVerdicts:       narvipg.NewReviewVerdictStore(pool),
				RepoSettings:         narvipg.NewRepoSettingsStore(pool),
				ReviewFindings:       narvipg.NewReviewFindingStore(pool),
				AutoApprovalOutcomes: narvipg.NewAutoApprovalOutcomeStore(pool),
				Timeouts:             platform.DefaultTimeouts(),
			},
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
	// Step 62 (§21.1/§21.2): a matching Shippable=auto review_verdicts
	// row, at this exact head sha, is now REQUIRED before the real
	// eligibility engine will ever say ok=true -- see
	// seedAutoApprovedVerdict's own doc comment (aggregate_integration_test.go)
	// for why every "eligible baseline" fixture in this package needs one.
	seedAutoApprovedVerdict(ctx, t, pool, repoFullName, int32(prNumber), pr.HeadSHA)
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

	// Paired with review:low-risk deliberately -- an otherwise-fully-
	// eligible risk label -- so needs-human is provably the ONE thing
	// keeping this refused, not a coincidental "no/unrecognized risk
	// label" refusal (§60 review TEST BATCH: this subtest and what used
	// to be a separate "NeedsHumanWithLowRisk_Refused" subtest were
	// byte-identical, both pairing needs-human with low-risk -- the
	// latter was deleted as pure duplication, adding zero coverage
	// despite its own comment's claim to cover "the OTHER half").
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
		// §60 review TEST BATCH: this subtest is DOUBLE-GATED by
		// ComputeAutoApprovalEligible's own IsDraft input, so deleting
		// RevalidateForMerge's explicit `if target.Draft` early return
		// still passes an `ok`-only/reason-non-empty assertion -- the PR
		// still refuses, just via the generic downstream eligibility
		// check instead, for a DIFFERENT reason. This exact trap already
		// cost one previous review round. Assert the reason NAMES
		// "draft" specifically -- the fallback eligibility-based
		// refusal's own reason text never mentions "draft" at all, so
		// only the explicit check produces this exact string.
		if !strings.Contains(reason, "draft") {
			t.Errorf("reason = %q, want it to name \"draft\" specifically (proves the explicit draft check fired, not a coincidental eligibility refusal for some unrelated reason)", reason)
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

	// Step 62 (§21.2) note: classifyPRLabels' own "most restrictive risk
	// label wins" property (§60 review finding A6) is no longer
	// observable through RevalidateForMerge's own ok/refused OUTCOME --
	// the real auto-approval eligibility engine (internal/domain/
	// autoapproval) gates on the STORED verdict's own Shippable field,
	// never on a PR's GitHub risk labels at all (those labels are now
	// purely a display artifact synced FROM a past verdict, §8.2/Step 47,
	// never fed back INTO eligibility). Coverage for classifyPRLabels'
	// own precedence rule moved to labels_test.go's own
	// TestClassifyPRLabels_MostRestrictiveRiskLabelWins, which asserts
	// the returned riskLabel value directly rather than through this
	// function's now-unrelated merge-eligibility outcome.

	// Step 62 (§21.2): a PR with NO review_verdicts row at all must be
	// refused -- the auto-approval engine fails CLOSED on "no verdict on
	// record", never defaulting to eligible for lack of data.
	t.Run("NoVerdictOnRecord_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-no-verdict"
		owner, repo, ok := reposource.SplitFullName(repoFullName)
		if !ok {
			t.Fatalf("malformed repoFullName %q", repoFullName)
		}
		htmlURL := "https://github.com/" + repoFullName + "/pull/30"
		pr := ports.OpenPR{
			Owner: owner, Repo: repo, Number: 30,
			Title: "no verdict yet", HTMLURL: htmlURL, HeadSHA: "sha-30",
			Assignees:    []ports.PRPerson{{ExternalID: actorGitHubID, Login: "actor"}},
			CIConclusion: ports.CIConclusionSuccess,
		}
		rs.sourceControl.openPRsByExternalID[actorGitHubID] = append(rs.sourceControl.openPRsByExternalID[actorGitHubID], pr)
		session, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
		if err != nil {
			t.Fatalf("create platform session: %v", err)
		}
		if _, err := narvipg.NewArtifactStore(pool).Create(ctx, sqlcgen.CreateArtifactParams{
			SessionID: session.ID, Type: sqlcgen.ArtifactTypePr, Url: htmlURL, Metadata: []byte("{}"),
		}); err != nil {
			t.Fatalf("create pr artifact: %v", err)
		}
		// Deliberately NO seedAutoApprovedVerdict call -- this is the
		// fact under test.

		ok2, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok2 {
			t.Fatal("RevalidateForMerge() ok = true, want false (no review_verdicts row exists for this PR)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	// Step 62 (§21.2): the stale-head-SHA guard -- a verdict on record
	// whose own head_sha no longer matches the PR's LIVE current head
	// must refuse, no matter how clean that verdict once looked.
	t.Run("StaleVerdictHeadSHA_Refused", func(t *testing.T) {
		const repoFullName = "acme/revalidate-stale-verdict"
		pr := rs.eligiblePR(ctx, t, pool, actorGitHubID, repoFullName, 31)
		// A new commit landed after the verdict was posted -- the live
		// PR's own head sha has moved on, but review_verdicts.head_sha
		// (seeded by eligiblePR against the OLD pr.HeadSHA) has not.
		pr.HeadSHA = "sha-31-newer-commit"
		rs.replaceTargetPR(actorGitHubID, pr)

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, repoFullName, pr.Number, "tok")
		if err != nil {
			t.Fatalf("RevalidateForMerge() error = %v, want nil", err)
		}
		if ok {
			t.Fatal("RevalidateForMerge() ok = true, want false (the verdict on record was produced against an earlier commit)")
		}
		if reason == "" {
			t.Error("reason is empty, want a human-readable explanation")
		}
	})

	// §60 review TEST BATCH (second round): RevalidateForMerge's own
	// truncated->500 branch was never executed by any existing test --
	// when the target PR is not found in a TRUNCATED (partial/degraded)
	// read, this must return a genuine ERROR (the httpapi handler maps
	// this to a 500, prompting a client retry), never the confident "not
	// assigned to you" 409 a complete-but-empty read would legitimately
	// warrant: a degraded read cannot tell "genuinely not yours" apart
	// from "we simply failed to see it". Deliberately the LAST subtest
	// in this test function -- it mutates the shared fake's own
	// openPRsTruncated flag, reset via defer, but sequencing it last
	// avoids any risk of bleeding into an EARLIER subtest were t.Run's
	// own declaration-order-execution guarantee ever to change.
	t.Run("TruncatedReadWithoutTargetPR_ReturnsErrorNeverAConfident409", func(t *testing.T) {
		rs.sourceControl.openPRsTruncated = true
		defer func() { rs.sourceControl.openPRsTruncated = false }()

		ok, _, reason, err := decisioninbox.RevalidateForMerge(ctx, rs.deps, rs.sourceControl, actorGitHubID, "acme/revalidate-truncated-not-found", 9999, "tok")
		if err == nil {
			t.Fatal("RevalidateForMerge() error = nil, want a non-nil error -- a truncated read must never confidently deny, it must fail loudly instead so the caller retries")
		}
		if ok {
			t.Error("RevalidateForMerge() ok = true, want false")
		}
		if reason != "" {
			t.Errorf("reason = %q, want empty (this is an error return, not a domain refusal reason)", reason)
		}
	})
}
