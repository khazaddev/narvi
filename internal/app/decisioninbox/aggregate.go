// This file (aggregate.go) implements Build -- the decision inbox's own
// read-model aggregation (Step 60, "decision inbox: read model + API",
// §16). A READ MODEL, not new state (§16.2): every fact below is derived
// from Postgres rows and SourceControl reads that already exist for other
// reasons; this package writes NOTHING back to Postgres and introduces no
// new table.
//
// # The four kinds, and exactly how each is decided
//
//   - KindReadyToMerge: an open, non-draft PR the actor is assigned to
//     (directly, as requested reviewer, or via CODEOWNERS), authored by a
//     platform session (an artifacts row of type 'pr' exists for this
//     PR's own URL -- internal/app/sessionactor/pushpr.go's own
//     recordPRArtifact, Step 21), CI green at head, and passing this
//     Step's own INTERIM auto-approval-eligibility stand-in (internal/
//     domain/decisioninbox.ComputeAutoApprovalEligible -- see that
//     function's own doc comment for the full justification: §21's real
//     engine, and review_verdicts to back it, do not exist yet as of this
//     Step, confirmed empirically before writing this file).
//   - KindNeedsReview: every OTHER open, non-draft, non-handoff,
//     non-§17-excluded PR the actor is assigned to -- i.e. the residual
//     bucket for an assigned PR that is not (yet) ready_to_merge. §16.1's
//     own prose names "verdict >= medium or a formal review is gated" as
//     the typical case; this Step reads that as descriptive of why a PR
//     usually lands here, not as an additional AND-gate that could leave
//     a PR the user is genuinely assigned to invisible in their own
//     inbox merely because, say, it has never been reviewed at all yet
//     (no risk label whatsoever) -- every legitimately assigned PR
//     appears SOMEWHERE.
//   - KindAwaitingApproval: plan-mode plans the actor is entitled to
//     approve (authz.Authorize(ActionApprovePlan, ...), the exact same
//     verdict httpapi.canActOnPlan already renders for the real approve/
//     reject endpoints) PLUS handoff items (§14.4): a PR the actor is
//     assigned to that also carries the "handoff" label rides this kind
//     instead of needs_review/ready_to_merge, since a handoff decision
//     ("send to engineering?") is not an ordinary code-review action.
//     KNOWN SCOPE LIMIT, documented rather than silently left a gap: this
//     Step discovers handoff PRs via the SAME assignee/requested-reviewer
//     mechanism as any other PR -- a handoff PR whose deciding PM is
//     neither assigned nor a requested reviewer on the resulting GitHub
//     PR will not surface here. §14.4's own v2 (a dedicated child-session
//     escalation) is explicitly deferred already ("only if handoff volume
//     justifies it"); building a SEPARATE discovery mechanism ahead of
//     that need would be speculative scope this Step does not add.
//   - KindNeedsAttention (ADMIN ONLY, §16.1's own parenthetical, enforced
//     by Build itself -- never populated for a non-admin actor): failed,
//     resumable sessions; automations auto-paused (consecutive_failures
//     at or past automation.AutoPauseThreshold when status is 'paused' --
//     the ONLY durable signal that distinguishes an auto-pause from a
//     direct admin PauseAutomation call, since both write the identical
//     status='paused' transition and this table carries no separate
//     pause-reason column); dead-lettered outbox deliveries.
//
// # Structural exclusion (§17)
//
// Every PR candidate is checked against sentinel_fixes.fix_pr_number
// (SentinelFixStore.ExistsByFixPRNumber) BEFORE it is ever classified into
// a kind -- a sentinel-auto-fix follow-up PR is excluded outright, never
// merely filtered by a label or a convention someone could forget to
// apply consistently (§16.1: "Make this a structural exclusion, not a
// filter someone can forget").

package decisioninbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/automation"
	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
	"github.com/khazaddev/narvi/internal/domain/handoff"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/platform"
)

// maxAttentionRowsPerSource bounds each of the three needs_attention
// sub-scans (failed sessions, paused automations, dead-letter outbox) --
// §21.1's own "bounded from day one" discipline.
const maxAttentionRowsPerSource = 100

// Deps bundles every dependency Build needs -- constructed once at
// process wiring time (cmd/control-plane/main.go), mirroring every other
// app-layer Deps struct in this codebase.
type Deps struct {
	Plans          *postgres.PlanStore
	Sessions       *postgres.SessionStore
	Participants   *postgres.ParticipantStore
	Automations    *postgres.AutomationStore
	Outbox         *postgres.OutboxStore
	ReviewFindings *postgres.ReviewFindingStore
	SentinelFixes  *postgres.SentinelFixStore
	Artifacts      *postgres.ArtifactStore
	Identities     *postgres.IdentityStore

	SCMCache *SCMCache

	TokenEncryptionKey []byte
	Timeouts           platform.Timeouts
}

// Result is Build's own return shape.
type Result struct {
	Items []Item

	// SCMAsOf is when the PR-derived items (ready_to_merge/needs_review)
	// were actually fetched from GitHub -- nil when the actor has no
	// usable linked GitHub credential (no PR items were attempted at
	// all), never a silent "now" standing in for a cache hit's own real,
	// earlier fetch instant (§16.2: "the response carries its as-of
	// timestamp... never presented as live truth").
	SCMAsOf *time.Time

	// SCMFetchFailed is the third state SCMAsOf==nil alone cannot express
	// (§60 review finding C1): true iff the actor DOES have a usable
	// linked GitHub credential but the live PR fetch itself failed
	// (buildPRItems returned an error -- a revoked token, a GitHub
	// incident, a timeout), as opposed to SCMAsOf==nil with
	// SCMFetchFailed==false, which means no linked identity was found at
	// all, so no SCM read was even ATTEMPTED. The contract (dtos.schema.
	// json) previously documented scmAsOf==null as meaning ONLY the
	// latter -- an outage was indistinguishable from "you have no GitHub
	// linked" to a contract-abiding client, which would render the wrong
	// empty state. Always false when SCMAsOf is non-nil (a successful
	// fetch and a failed one are mutually exclusive within one Build
	// call).
	SCMFetchFailed bool

	// DecisionLatencyMedian/DecisionLatencySampleSize/
	// DecisionLatencyComputed mirror §21.1's own "not yet computed
	// sentinel, distinct from a real zero" discipline for every other
	// analytics rollup in this codebase -- DecisionLatencyComputed=false
	// means "no data in the window", never rendered identically to a
	// real, computed zero-second median.
	DecisionLatencyMedian     time.Duration
	DecisionLatencySampleSize int
	DecisionLatencyComputed   bool
}

// Build assembles, ranks, and returns the full decision inbox for
// (actorUserID, actorRole) as of now -- see this file's own top doc
// comment for the full per-kind design.
func Build(ctx context.Context, deps Deps, actorUserID pgtype.UUID, actorRole authz.Role, now time.Time) (Result, error) {
	logger := platform.Logger(ctx)

	var items []Item
	var scmAsOf *time.Time
	var scmFetchFailed bool

	if login, token, ok := resolveActorGitHubCredential(ctx, deps, actorUserID); ok {
		prItems, asOf, err := buildPRItems(ctx, deps, login, token, now)
		if err != nil {
			logger.Error("decisioninbox: build pr items failed", "error", err)
			scmFetchFailed = true
		} else {
			items = append(items, prItems...)
			scmAsOf = &asOf
		}
	}

	planItems, err := buildPlanItems(ctx, deps, actorUserID, actorRole, now)
	if err != nil {
		logger.Error("decisioninbox: build plan items failed", "error", err)
	} else {
		items = append(items, planItems...)
	}

	if actorRole == authz.RoleAdmin {
		items = append(items, buildAttentionItems(ctx, deps, now, logger)...)
	}

	items = rank(items)

	median, sampleSize, computed, err := Metrics(ctx, deps, now)
	if err != nil {
		logger.Error("decisioninbox: compute decision latency failed", "error", err)
	}

	return Result{
		Items:                     items,
		SCMAsOf:                   scmAsOf,
		SCMFetchFailed:            scmFetchFailed,
		DecisionLatencyMedian:     median,
		DecisionLatencySampleSize: sampleSize,
		DecisionLatencyComputed:   computed,
	}, nil
}

// rank sorts items by internal/domain/decisioninbox's own decision-cost-
// then-age ordering (§16.1).
func rank(items []Item) []Item {
	keys := make([]decisioninbox.RankKey, len(items))
	for i, it := range items {
		keys[i] = decisioninbox.RankKey{Kind: it.Kind, EnteredQueueAt: it.EnteredQueueAt}
	}
	order := decisioninbox.SortIndex(keys)
	sorted := make([]Item, len(items))
	for i, idx := range order {
		sorted[i] = items[idx]
	}
	return sorted
}

// resolveActorGitHubCredential fetches actorUserID's own linked GitHub
// identity and decrypts its stored OAuth token -- mirrors httpapi.
// ApplySuggestion's own identical decrypt-and-use pattern
// (reviewfindings.go) exactly, applied here to the CURRENT actor rather
// than an acting maintainer on a specific finding. ok=false (no linked
// GitHub identity, or a decrypt failure) means this actor's PR-derived
// items are simply skipped -- never an error that fails the whole Build
// call, since plan/attention items still have plenty to show independent
// of any GitHub credential.
func resolveActorGitHubCredential(ctx context.Context, deps Deps, actorUserID pgtype.UUID) (externalID, token string, ok bool) {
	identity, err := deps.Identities.GetByUserAndProvider(ctx, actorUserID, sqlcgen.IdentityProviderGithub)
	if err != nil {
		return "", "", false
	}
	if identity.AccessTokenEncrypted == nil {
		return "", "", false
	}
	plaintext, err := platform.DecryptToken(deps.TokenEncryptionKey, identity.AccessTokenEncrypted)
	if err != nil {
		return "", "", false
	}
	// Never logged, here or anywhere it might propagate to -- mirrors
	// scmcredentials.go/reviewfindings.go's own identical discipline.
	return identity.ExternalID, string(plaintext), true
}

// buildPRItems fetches and classifies every open PR the actor is
// currently assigned to -- see this file's own top doc comment for the
// full ready_to_merge/needs_review/handoff decision and the §17
// structural exclusion.
func buildPRItems(ctx context.Context, deps Deps, actorGitHubID, token string, now time.Time) ([]Item, time.Time, error) {
	prs, asOf, err := deps.SCMCache.ListOpenPRsForUser(ctx, ports.ListOpenPRsForUserSpec{GitHubExternalID: actorGitHubID, Token: token}, now)
	if err != nil {
		return nil, time.Time{}, err
	}

	// One fresh budget per Build call -- see codeOwnersBudget's own doc
	// comment (§60 review finding B3) for why this must never be shared
	// across actors/requests the way deps.SCMCache itself is.
	budget := newCodeOwnersBudget(maxCodeOwnerResolutionsPerBuild)

	items := make([]Item, 0, len(prs))
	for _, pr := range prs {
		if pr.Draft {
			continue
		}

		repoFullName := pr.Owner + "/" + pr.Repo

		excluded, existsErr := deps.SentinelFixes.ExistsByFixPRNumber(ctx, repoFullName, int32(pr.Number))
		if existsErr != nil {
			// §17's structural exclusion must fail CLOSED (§60 review
			// finding A3): a store error means "cannot prove this is NOT
			// a sentinel-auto-fix follow-up", treated identically to a
			// CONFIRMED one below -- excluded outright, never best-effort
			// passed through as if the check had simply found nothing.
			// isPlatformAuthored (below) already fails closed this same
			// way; this now matches it.
			platform.Logger(ctx).Error("decisioninbox: check sentinel-fix exclusion failed -- failing closed (excluding this pr)", "error", existsErr, "repo", repoFullName, "pr_number", pr.Number)
			continue
		}
		if excluded {
			continue // §17 structural exclusion -- never a row, regardless of any other criterion.
		}

		item := buildPROpenItem(ctx, deps, pr, repoFullName, actorGitHubID, token, now, budget)
		items = append(items, item)
	}

	return items, asOf, nil
}

// openFindingsUnknownFailClosed is the OpenBlockingFindings value
// buildPROpenItem substitutes when countOpenFindings itself errors (§60
// review finding A1) -- any positive value fails ComputeAutoApprovalEligible
// closed (its own check is a bare "> 0"), so the exact magnitude carries
// no further meaning beyond that; 1 is chosen purely so a human reading a
// rendered row sees a small, plausible-looking finding count rather than
// an obviously-synthetic sentinel like MaxInt32.
const openFindingsUnknownFailClosed = 1

// buildPROpenItem assembles one Item for an already-filtered, non-draft,
// non-§17-excluded OpenPR.
func buildPROpenItem(ctx context.Context, deps Deps, pr ports.OpenPR, repoFullName, actorGitHubID, token string, now time.Time, budget *codeOwnersBudget) Item {
	provenance := resolvePRProvenance(ctx, deps, pr, repoFullName, actorGitHubID, token, now, budget)

	hasNeedsHuman, riskLabel, isHandoffPR := classifyPRLabels(pr.Labels)

	openFindings, findingsErr := countOpenFindings(ctx, deps, repoFullName, pr.Number)
	if findingsErr != nil {
		// Fail CLOSED (§60 review finding A1) -- see countOpenFindings'
		// own doc comment for why a degraded zero here would be actively
		// dangerous, not merely imprecise.
		platform.Logger(ctx).Error("decisioninbox: count open findings failed -- failing closed (treated as a blocking finding present)", "error", findingsErr, "repo", repoFullName, "pr_number", pr.Number)
		openFindings = openFindingsUnknownFailClosed
	}
	ciGreen := pr.CIConclusion == ports.CIConclusionSuccess

	item := Item{
		RepoFullName:       repoFullName,
		PRNumber:           pr.Number,
		Title:              pr.Title,
		HTMLURL:            pr.HTMLURL,
		HeadSHA:            pr.HeadSHA,
		Provenance:         &provenance,
		RiskLabel:          riskLabel,
		CIGreen:            ciGreen,
		Findings:           openFindings,
		IsHandoff:          isHandoffPR,
		HasApprovingReview: pr.HasApprovingReview,
		EnteredQueueAt:     pr.CreatedAt,
	}
	item.AgeSeconds = int64(decisioninbox.Age(item.EnteredQueueAt, now).Seconds())
	item.Stale = decisioninbox.IsStale(item.EnteredQueueAt, now, deps.Timeouts.DecisionInboxStaleAfter)

	switch {
	case isHandoffPR:
		item.Kind = decisioninbox.KindAwaitingApproval
	default:
		platformAuthored := isPlatformAuthored(ctx, deps, pr.HTMLURL)
		eligible := decisioninbox.ComputeAutoApprovalEligible(decisioninbox.EligibilityInput{
			CIGreen:              ciGreen,
			IsDraft:              pr.Draft,
			RiskLabel:            riskLabel,
			HasNeedsHumanLabel:   hasNeedsHuman,
			OpenBlockingFindings: openFindings,
		})
		if platformAuthored && eligible {
			item.Kind = decisioninbox.KindReadyToMerge
		} else {
			item.Kind = decisioninbox.KindNeedsReview
		}
	}

	return item
}

// resolvePRProvenance determines WHY pr is assigned to actorGitHubID --
// §16.1's own "a first-class field" assignment provenance.
func resolvePRProvenance(ctx context.Context, deps Deps, pr ports.OpenPR, repoFullName, actorGitHubID, token string, now time.Time, budget *codeOwnersBudget) decisioninbox.Provenance {
	in := decisioninbox.ProvenanceInput{RepoFullName: repoFullName}

	for _, a := range pr.Assignees {
		if a.ExternalID == actorGitHubID {
			in.DirectlyAssigned = true
			break
		}
	}
	for _, r := range pr.RequestedReviewers {
		if r.ExternalID == actorGitHubID {
			in.RequestedReviewer = true
			break
		}
	}

	// budget.take gates this call at zero I/O once the per-build
	// CODEOWNERS-resolution cap is exhausted (§60 review finding B3) --
	// see codeOwnersBudget's own doc comment. Skipping it here only ever
	// degrades a display nicety (this ONE PR's provenance falls back to
	// the "un-pinned requested reviewer" default below, or plain
	// ProvenanceRequestedReviewer/ProvenanceDirect if either of those
	// already matched) -- CODEOWNERS resolution is never how a PR is
	// DISCOVERED (searchOpenPRs' own doc comment), so skipping it can
	// never hide a row or grant unintended access.
	//
	// Ref is pr.BaseRef, deliberately never pr.HeadSHA (§60 review
	// finding B3's own related hardening): the PR's HEAD is attacker-
	// chosen (whoever opened/pushed the PR controls it), so resolving
	// CODEOWNERS there would let a PR's own author dictate which
	// CODEOWNERS file this call reads for classifying THEIR OWN PR --
	// GitHub's own real CODEOWNERS enforcement is evaluated against the
	// repo's base branch, never a PR's head, and this now matches that.
	if budget.take(ctx) {
		if owners, _, err := deps.SCMCache.ResolveCodeOwners(ctx, ports.ResolveCodeOwnersSpec{
			Owner: pr.Owner, Repo: pr.Repo, Ref: pr.BaseRef, Paths: pr.ChangedFiles, Token: token,
		}, now); err == nil {
			for _, o := range owners {
				if o.ExternalID == actorGitHubID {
					in.CodeOwnerMatch = true
					in.CodeOwnerPattern = o.Pattern
					break
				}
			}
		}
	}

	if !in.DirectlyAssigned && !in.RequestedReviewer && !in.CodeOwnerMatch {
		// This PR was only discoverable at all via the review-requested:
		// search qualifier matching through TEAM membership GitHub itself
		// resolved server-side (listopenprs.go's own top doc comment: "If
		// the requested person is on a team that is requested for review,
		// then review requests for that team will also appear in the
		// search results") -- this codebase has no further API surface
		// wired to re-derive WHICH team without a second, separate
		// team-membership listing call this Step's own scope does not add.
		// Reported as an un-pinned "requested reviewer" rather than
		// silently falling through to ResolveProvenance's own not-ok
		// zero value, which would incorrectly suggest this row should
		// never have appeared in the inbox at all.
		in.RequestedReviewer = true
	}

	provenance, _ := decisioninbox.ResolveProvenance(in)
	return provenance
}

// classifyPRLabels scans pr's own current labels for the three signals
// this Step's own interim eligibility/handoff classification needs.
//
// riskLabel picks the MOST RESTRICTIVE of the review:*-risk labels
// present -- any high-risk label wins over medium, which wins over low
// (§60 review finding A6). GitHub's own labels array carries NO ordering
// guarantee a caller may rely on (verified directly: this codebase's own
// verdictnotifier.go issues AddLabels then a SEPARATE per-label
// RemoveLabel call, two independent GitHub calls -- a failed Remove
// durably leaves BOTH an old and a new risk label on the same PR at
// once, a genuinely reachable state, not a hypothetical), so picking
// "whichever label happens to appear last in the slice" would authorize
// a merge decision on an unspecified, externally-controlled ordering.
// This mirrors reviewpost.RiskLabel/review.baselineFromRisk's own
// already-established "unrecognized/ambiguous fails conservative toward
// the more alarming tier" convention, applied here to "more than one
// tier present at once" instead of "no tier recognized at all".
func classifyPRLabels(labels []string) (hasNeedsHuman bool, riskLabel string, isHandoff bool) {
	for _, l := range labels {
		switch l {
		case reviewpost.LabelNeedsHuman:
			hasNeedsHuman = true
		case reviewpost.LabelHighRisk:
			riskLabel = reviewpost.LabelHighRisk
		case reviewpost.LabelMediumRisk:
			if riskLabel != reviewpost.LabelHighRisk {
				riskLabel = reviewpost.LabelMediumRisk
			}
		case reviewpost.LabelLowRisk:
			if riskLabel == "" {
				riskLabel = reviewpost.LabelLowRisk
			}
		case handoff.Label:
			isHandoff = true
		}
	}
	return hasNeedsHuman, riskLabel, isHandoff
}

// maxCodeOwnerResolutionsPerBuild bounds the TOTAL number of
// ResolveCodeOwners calls ONE Build invocation will make across every
// candidate PR -- the per-inbox-build half of B3's own two-layer cap
// (the adapter's own maxCodeOwnerRefsPerCall, githubapi/
// resolvecodeowners.go, bounds a SINGLE PR's own CODEOWNERS fan-out;
// this bounds the SUM across up to maxOpenPRsForUser PRs in one page
// load) -- §60 review finding B3: a victim whose review is requested on
// many PRs, each carrying a moderately large CODEOWNERS file, still
// cannot drive an unbounded number of outbound calls on the victim's own
// token in one inbox load.
const maxCodeOwnerResolutionsPerBuild = 200

// codeOwnersBudget bounds how many MORE ResolveCodeOwners calls the
// CURRENT Build invocation is still willing to make. A fresh budget is
// created once per Build call (buildPRItems) and threaded down through
// buildPROpenItem/resolvePRProvenance -- it must NEVER be shared across
// actors/requests the way deps.SCMCache itself is (SCMCache is
// constructed once, process-wide, at wiring time): a budget living
// there would leak across unrelated users' own inbox loads instead of
// bounding each one independently, exactly the per-victim isolation
// this cap exists to provide.
type codeOwnersBudget struct {
	remaining       int
	truncatedLogged bool
}

// newCodeOwnersBudget builds a fresh, per-Build-call budget.
func newCodeOwnersBudget(limit int) *codeOwnersBudget {
	return &codeOwnersBudget{remaining: limit}
}

// take reports whether the caller may still make one more
// ResolveCodeOwners call this Build invocation -- false once the
// per-build cap is exhausted, in which case the FIRST such call logs a
// warning (never silently); every later call this same Build invocation
// makes after that stays silent, so one truncated inbox load produces
// exactly one log line, not one per remaining PR.
func (b *codeOwnersBudget) take(ctx context.Context) bool {
	if b.remaining <= 0 {
		if !b.truncatedLogged {
			platform.Logger(ctx).Warn("decisioninbox: codeowners resolution budget exhausted for this inbox build -- remaining prs' codeowners provenance will be skipped", "max_per_build", maxCodeOwnerResolutionsPerBuild)
			b.truncatedLogged = true
		}
		return false
	}
	b.remaining--
	return true
}

// countOpenFindings counts repoFullName/prNumber's own STILL-OPEN
// (reviewpost.FindingStatusOpen) review_findings rows -- a rebutted or
// fix-pending/open/merged/applied finding does not count (each already
// has an explicit resolution).
//
// A fetch failure is propagated to the caller (§60 review finding A1) --
// it must NEVER degrade to zero. This function's own doc comment used to
// justify a degraded zero here by claiming "this Step's own eligibility
// check already requires the risk label to be exactly LabelLowRisk
// before this count matters at all" -- that reasoning was backwards:
// ComputeAutoApprovalEligible's OpenBlockingFindings > 0 check and its
// RiskLabel == LabelLowRisk check are two INDEPENDENT AND-conditions,
// not one gated behind the other -- a low-risk PR WITH a genuinely open,
// unresolved finding is EXACTLY the population this count exists to
// keep out of ready_to_merge, so a degraded zero on a store error would
// silently flip that PR from ineligible to eligible, the opposite of
// this codebase's own fail-conservative discipline (isPlatformAuthored,
// this same file, already fails closed on ITS OWN store error -- this
// now matches it). Each of this function's own two callers fails closed
// in the way appropriate to its own context: aggregate.go's
// buildPROpenItem degrades the affected row to non-ready_to_merge rather
// than failing the whole read model; revalidate.go's RevalidateForMerge
// propagates the error outright, refusing the merge.
func countOpenFindings(ctx context.Context, deps Deps, repoFullName string, prNumber int) (int, error) {
	findings, err := deps.ReviewFindings.ListOpenAndRebutted(ctx, repoFullName, int32(prNumber))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, f := range findings {
		if f.Status == string(reviewpost.FindingStatusOpen) {
			count++
		}
	}
	return count, nil
}

// isPlatformAuthored reports whether SOME Narvi session pushed and opened
// the PR at htmlURL (§16.1's own "authored by a platform session"
// ready_to_merge criterion) -- see ArtifactStore.GetPRArtifactByURL's own
// doc comment.
func isPlatformAuthored(ctx context.Context, deps Deps, htmlURL string) bool {
	_, err := deps.Artifacts.GetPRArtifactByURL(ctx, htmlURL)
	return err == nil
}

// buildPlanItems returns every plan-mode plan actorUserID/actorRole is
// entitled to approve (authz.ActionApprovePlan, the SAME verdict
// httpapi.canActOnPlan already renders for the real approve/reject
// endpoints).
func buildPlanItems(ctx context.Context, deps Deps, actorUserID pgtype.UUID, actorRole authz.Role, now time.Time) ([]Item, error) {
	rows, err := deps.Plans.ListAwaitingApproval(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		ownedOrJoined := row.SessionCreatedBy.Valid && row.SessionCreatedBy == actorUserID
		if !ownedOrJoined {
			if exists, err := deps.Participants.Exists(ctx, row.SessionID, actorUserID); err == nil && exists {
				ownedOrJoined = true
			}
		}

		actor := authz.Actor{UserID: actorUserID.String(), Role: actorRole}
		if err := authz.Authorize(actor, authz.ActionApprovePlan, authz.Resource{OwnedOrJoined: ownedOrJoined}); err != nil {
			continue
		}

		title := "plan-mode plan"
		if row.SessionTitle != nil && *row.SessionTitle != "" {
			title = *row.SessionTitle
		}

		item := Item{
			Kind:             decisioninbox.KindAwaitingApproval,
			Title:            title,
			PlanID:           row.ID.String(),
			SessionID:        row.SessionID.String(),
			PlanSessionTitle: title,
		}
		if row.CreatedAt.Valid {
			item.EnteredQueueAt = row.CreatedAt.Time
		}
		item.AgeSeconds = int64(decisioninbox.Age(item.EnteredQueueAt, now).Seconds())
		item.Stale = decisioninbox.IsStale(item.EnteredQueueAt, now, deps.Timeouts.DecisionInboxStaleAfter)

		items = append(items, item)
	}

	return items, nil
}

// buildAttentionItems returns every needs_attention row -- ADMIN ONLY;
// Build's own caller never invokes this for a non-admin actor. Each of
// the three sub-scans is independently best-effort: one failing is logged
// and simply contributes no rows, never failing the other two.
func buildAttentionItems(ctx context.Context, deps Deps, now time.Time, logger *slog.Logger) []Item {
	var items []Item

	sessions, err := deps.Sessions.ListFailed(ctx, maxAttentionRowsPerSource)
	if err != nil {
		logger.Error("decisioninbox: list failed sessions failed", "error", err)
	}
	for _, s := range sessions {
		title := "session"
		if s.Title != nil && *s.Title != "" {
			title = *s.Title
		}
		failureReason := ""
		if s.FailureReason != nil {
			failureReason = string(*s.FailureReason)
		}
		item := Item{
			Kind:          decisioninbox.KindNeedsAttention,
			Title:         title,
			SessionID:     s.ID.String(),
			FailureReason: failureReason,
		}
		if s.UpdatedAt.Valid {
			item.EnteredQueueAt = s.UpdatedAt.Time
		}
		item.AgeSeconds = int64(decisioninbox.Age(item.EnteredQueueAt, now).Seconds())
		item.Stale = decisioninbox.IsStale(item.EnteredQueueAt, now, deps.Timeouts.DecisionInboxStaleAfter)
		items = append(items, item)
	}

	pausedStatus := sqlcgen.AutomationStatusPaused
	automations, err := deps.Automations.List(ctx, pgtype.UUID{}, &pausedStatus)
	if err != nil {
		logger.Error("decisioninbox: list paused automations failed", "error", err)
	}
	for _, a := range automations {
		// A manually-paused automation (httpapi.PauseAutomation) writes
		// the IDENTICAL status='paused' transition an auto-pause does --
		// this table carries no separate pause-REASON column (migrations/
		// 000051_automations.up.sql). ConsecutiveFailures reaching
		// automation.AutoPauseThreshold is the one durable, positive
		// signal that distinguishes "the strike mechanism paused this"
		// from "an admin chose to pause it" -- a manual pause well before
		// any strikes accumulated leaves ConsecutiveFailures below the
		// threshold, correctly excluded here.
		if a.ConsecutiveFailures < int32(automation.AutoPauseThreshold) {
			continue
		}
		summary := ""
		if a.ArtifactSummary != nil {
			summary = *a.ArtifactSummary
		}
		item := Item{
			Kind:            decisioninbox.KindNeedsAttention,
			Title:           a.Name,
			AutomationID:    a.ID.String(),
			ArtifactSummary: summary,
		}
		if a.UpdatedAt.Valid {
			item.EnteredQueueAt = a.UpdatedAt.Time
		}
		item.AgeSeconds = int64(decisioninbox.Age(item.EnteredQueueAt, now).Seconds())
		item.Stale = decisioninbox.IsStale(item.EnteredQueueAt, now, deps.Timeouts.DecisionInboxStaleAfter)
		items = append(items, item)
	}

	entries, err := deps.Outbox.ListDeadLetter(ctx, maxAttentionRowsPerSource)
	if err != nil {
		logger.Error("decisioninbox: list dead-letter outbox entries failed", "error", err)
	}
	for _, e := range entries {
		lastErr := ""
		if e.LastError != nil {
			lastErr = *e.LastError
		}
		item := Item{
			Kind:       decisioninbox.KindNeedsAttention,
			Title:      fmt.Sprintf("outbox delivery: %s", e.Kind),
			OutboxID:   e.ID.String(),
			OutboxKind: e.Kind,
			LastError:  lastErr,
		}
		if e.CreatedAt.Valid {
			item.EnteredQueueAt = e.CreatedAt.Time
		}
		item.AgeSeconds = int64(decisioninbox.Age(item.EnteredQueueAt, now).Seconds())
		item.Stale = decisioninbox.IsStale(item.EnteredQueueAt, now, deps.Timeouts.DecisionInboxStaleAfter)
		items = append(items, item)
	}

	return items
}
