package outboxworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/provenance"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file implements Step 48's own ("sentinels + suggestions", §17.2)
// sentinel-auto-fix notifier: ports.NotificationKindSentinelAutoFix's own
// real Deliver -- spawning the child session §17.2 describes. Lives in
// internal/app/outboxworker, NOT internal/app/sessionactor, for the exact
// reason this Step's own design settled on: sessionactor cannot import
// httpapi (§24.3, TECHNICAL_PLAN.md:846, already documents this
// constraint for a structurally identical problem), but this notifier
// MUST call httpapi.SpawnChildSession (the one sanctioned way to create a
// session with a parent/provenance tag, childsession.go) -- outboxworker
// is already the "real outbound network/side-effect work, never
// synchronously in the HTTP request" layer every other Notifier in this
// package lives at, and is free to import httpapi the same way internal/
// adapters/inbound/github's own coalesce.go already does (that package's
// own doc comment: "already callable from outside httpapi by design").

// sentinelFixBranchPrefix names Deliver's own generated, distinct fix-
// branch names -- confirmed-finding fix, see Deliver's own doc comment
// below for the full "why". Deliberately mirrors internal/domain/gitstate.
// sessionBranchPrefix's own "narvi/" convention (a fixed, invented prefix,
// vanishingly unlikely to collide with any real branch a human or
// automation might have already created) rather than inventing an
// unrelated one.
const sentinelFixBranchPrefix = "narvi/sentinel-fix/"

// sentinelFixBranchName derives the distinct upstream branch name Deliver
// creates (via SourceControl.CreateBranch) and has the fix child session
// check out and push to -- NEVER the origin PR's own head branch. Keyed
// off sentinelFixID (sentinel_fixes.id, already unique per (repo, PR)):
// deterministic and idempotent -- a redelivered outbox entry for the SAME
// claim computes the SAME branch name every time, never a fresh one per
// attempt (load-bearing for CreateBranch's own idempotent "already
// exists" handling, and for a retried Deliver to converge on the SAME
// child-session repo config rather than a different one each attempt).
func sentinelFixBranchName(sentinelFixID string) string {
	return sentinelFixBranchPrefix + sentinelFixID
}

// sentinelAutoFixNotifier implements ports.Notifier for
// ports.NotificationKindSentinelAutoFix.
type sentinelAutoFixNotifier struct {
	pool           *pgxpool.Pool
	sessions       *postgres.SessionStore
	turns          *postgres.TurnStore
	environments   *postgres.EnvironmentStore
	auditLog       *postgres.AuditLogStore
	registry       *sessionactor.Registry
	sentinelFixes  *postgres.SentinelFixStore
	reviewFindings *postgres.ReviewFindingStore
	// sourceControl/githubBotToken (confirmed-finding fix) let Deliver
	// create the fix session's own distinct upstream branch (see this
	// file's own Deliver doc comment) BEFORE ever spawning the child
	// session -- the SAME bot-attributed, static credential
	// createSentinelFixPRBestEffort (pushpr.go) already authenticates its
	// own fix-PR-creation calls with, never a per-user OAuth token (this
	// session has no human creator to decrypt one for).
	sourceControl  ports.SourceControl
	githubBotToken string
	timeouts       platform.Timeouts
}

var _ ports.Notifier = (*sentinelAutoFixNotifier)(nil)

// NewSentinelAutoFixNotifier builds a ports.Notifier for
// ports.NotificationKindSentinelAutoFix -- called once by cmd/control-
// plane/main.go's own kind->Notifier map assembly, mirroring every other
// notifier constructor's own identical "called exactly once" precedent.
// sourceControl/githubBotToken/timeouts are the SAME instances/values
// production wiring already constructs for every other GitHub-flavored
// notifier (e.g. githubapi.NewVerdictNotifier's own sourceControl/
// cfg.GitHubBotToken, cmd/control-plane/main.go).
func NewSentinelAutoFixNotifier(
	pool *pgxpool.Pool,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	environments *postgres.EnvironmentStore,
	auditLog *postgres.AuditLogStore,
	registry *sessionactor.Registry,
	sentinelFixes *postgres.SentinelFixStore,
	reviewFindings *postgres.ReviewFindingStore,
	sourceControl ports.SourceControl,
	githubBotToken string,
	timeouts platform.Timeouts,
) ports.Notifier {
	return &sentinelAutoFixNotifier{
		pool: pool, sessions: sessions, turns: turns, environments: environments,
		auditLog: auditLog, registry: registry, sentinelFixes: sentinelFixes, reviewFindings: reviewFindings,
		sourceControl: sourceControl, githubBotToken: githubBotToken, timeouts: timeouts,
	}
}

// sentinelAutoFixPromptText builds the fix session's own deterministic,
// server-rendered first prompt (never a raw pass-through of the finding's
// own agent-authored text alone) -- names the specific finding(s) this
// session exists to remediate, and states the two constraints §17.2
// requires explicitly (test/doc files only; build mode, no plan-mode
// gate) so the agent's own first turn has an honest, actionable brief
// even before it inspects the origin diff itself.
func sentinelAutoFixPromptText(descriptions []string) string {
	var b strings.Builder
	b.WriteString("You are a sentinel-auto-fix remediation session (Narvi, §17). ")
	b.WriteString("Your ONLY job is to fix the following coverage/doc-drift finding(s) from the origin pull request's own review, by adding the missing test(s) or updating the stale documentation -- nothing else:\n\n")
	for _, d := range descriptions {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	b.WriteString("\nYour write access is restricted to test and documentation files only. Do not modify any other file. Once your fix is complete and any relevant tests pass, push your branch -- a pull request against the origin branch will be opened automatically.")
	return b.String()
}

// Deliver implements ports.Notifier: unmarshals payload, checks
// idempotency (a child session already spawned for this claim -- a
// redelivered/retried outbox entry is a no-op, never a double-spawn),
// creates the fix session's own distinct upstream branch, then spawns the
// child session and records it back onto BOTH the sentinel_fixes row and
// every finding it addresses.
//
// # Confirmed-finding fix: the fix session needs its OWN branch
//
// payload.OriginHeadBranch is captured once, at claim time (reviewverdict.
// go), as the LITERAL value the eventual fix PR's own Base will be
// assigned to (ports.SentinelAutoFixPayload's own doc comment) -- a real
// re-review confirmed that this same string was ALSO being handed
// straight to the child session's own repos[].branch, unchanged. Since a
// session's repos[].branch is the ONE field that decides both what a
// fresh boot's own `git clone --branch <name>` checks out AND what the
// eventual `git push` sends back to the SAME-named remote ref
// (internal/sandboxagent/gitclone.CloneAll / cmd/sandbox-agent's own
// pushOneRepo -- neither ever invents a distinct branch for a fresh
// clone), that meant the fix session cloned, committed onto, and pushed
// back to the ORIGIN PR's own live branch -- silently fast-forwarding a
// still-open, still-under-review pull request with an unreviewed
// automated commit -- and its OWN eventual fix-PR CreatePR call
// (createSentinelFixPRBestEffort, pushpr.go) was doomed to Head == Base,
// which GitHub's real API rejects outright ("no commits between X and
// X"), so the stacked-fix PR could never actually open at all.
//
// The fix: BEFORE ever spawning the child session, resolve
// payload.OriginHeadBranch's own CURRENT commit SHA (SourceControl.
// ResolveBranchSHA) and create a brand-new branch ref at that exact SHA
// (SourceControl.CreateBranch, sentinelFixBranchName's own deterministic
// name) -- content-identical to the origin branch's own tip at claim time,
// but a name that exists ONLY for this fix session to check out, commit
// onto, and push. Everything downstream (the boot-time clone/checkout,
// the eventual push, and createSentinelFixPRBestEffort's own Head:
// pushed.Branch) then automatically uses this NEW, distinct name with NO
// further changes anywhere else -- fix.OriginHeadBranch (the eventual PR's
// own Base) is completely untouched, so Head != Base by construction, and
// the origin PR's own branch is never touched by this flow at all.
//
// A failure to resolve the origin branch's SHA or create the new branch
// is returned as a real error (never silently falls back to
// payload.OriginHeadBranch, which would reproduce the exact bug this
// fixes) -- the outbox worker's own existing backoff/retry machinery
// retries this delivery later; nothing user-visible has happened yet (no
// child session, no push, no PR), so a retry is safe.
func (n *sentinelAutoFixNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	if notification.Kind != ports.NotificationKindSentinelAutoFix {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: unrecognized notification kind %q", notification.Kind)
	}

	var payload ports.SentinelAutoFixPayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: decode payload: %w", err)
	}

	var fixID pgtype.UUID
	if err := fixID.Scan(payload.SentinelFixID); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: malformed sentinelFixId %q: %w", payload.SentinelFixID, err)
	}

	fix, err := n.sentinelFixes.GetByID(ctx, fixID)
	if err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: get sentinel_fixes row: %w", err)
	}
	if fix.FixChildSessionID.Valid {
		// Already spawned by an earlier delivery attempt (or a race with
		// another qualifying finding's own claim) -- idempotent no-op,
		// never a second child session for the SAME claim.
		return nil
	}

	var parentSessionID pgtype.UUID
	if err := parentSessionID.Scan(payload.OriginReviewSessionID); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: malformed originReviewSessionId %q: %w", payload.OriginReviewSessionID, err)
	}

	branch, err := n.createFixBranch(ctx, payload)
	if err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: create fix session's own upstream branch: %w", err)
	}

	prompt := sentinelAutoFixPromptText(payload.FindingDescriptions)
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{
				Name: payload.RepoName,
				Url:  payload.RepoCloneURL,
				// Branch here is the CHILD SESSION's own head branch to
				// check out and push FROM -- confirmed-finding fix: this is
				// now the brand-new, distinct branch createFixBranch just
				// created (content-identical to the origin branch's own tip
				// at claim time, so the fix session's own commits still
				// necessarily apply on top of the origin diff, exactly what
				// a stacked fix requires, §17.2) -- NEVER payload.
				// OriginHeadBranch's own literal value, which is reserved
				// for the eventual fix PR's own Base (pushpr.go's
				// createSentinelFixPRBestEffort) and must stay a genuinely
				// DIFFERENT branch from this one for that PR to ever open
				// at all.
				Branch: restdtos.CreateSessionRequestReposElemBranch(&branch),
			},
		},
	}

	childSession, cerr := httpapi.SpawnChildSession(ctx, n.pool, n.sessions, n.turns, n.environments, n.auditLog, n.registry, req, parentSessionID, 1, provenance.SentinelAutoFix)
	if cerr != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: spawn child session: %s", cerr.Message)
	}

	if _, err := n.sentinelFixes.UpdateChildSession(ctx, fix.ID, childSession.ID); err != nil {
		return fmt.Errorf("outboxworker: sentinelAutoFixNotifier: record child session on sentinel_fixes: %w", err)
	}

	for _, hash := range payload.FindingIdentityHashes {
		if _, err := n.reviewFindings.MarkFixPending(ctx, payload.RepoFullName, payload.OriginPRNumber, hash, childSession.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			// Best-effort per-finding: one finding's own row having since
			// disappeared/changed must not fail the whole delivery (the
			// child session itself is already correctly spawned and
			// recorded above) -- logged by the delivery worker's own
			// caller (builder.go's attempt), not here (this package's own
			// Notifier implementations carry no logger of their own,
			// matching every sibling notifier's identical convention).
			continue
		}
	}

	return nil
}

// createFixBranch resolves payload.OriginHeadBranch's own current commit
// SHA and creates a brand-new, distinct branch ref at that exact SHA --
// see Deliver's own doc comment above for the full "why". Returns the new
// branch's own name on success. Bounded by n.timeouts.RepoSHAResolutionTimeout
// for each of its two real outbound GitHub API calls, mirroring
// internal/app/sessionactor/pushpr.go's own resolvePRBaseBranch precedent
// for the identical class of call.
func (n *sentinelAutoFixNotifier) createFixBranch(ctx context.Context, payload ports.SentinelAutoFixPayload) (string, error) {
	if n.sourceControl == nil {
		return "", errors.New("no SourceControl configured")
	}

	if err := reposource.CheckRepoHost(payload.RepoCloneURL, ports.SupportedSourceControlHosts()...); err != nil {
		return "", fmt.Errorf("repo url does not name a supported source-control host: %w", err)
	}
	owner, repoName, err := reposource.ParseOwnerRepo(payload.RepoCloneURL)
	if err != nil {
		return "", fmt.Errorf("parse owner/repo from clone url: %w", err)
	}

	shaCtx, cancel := context.WithTimeout(ctx, n.timeouts.RepoSHAResolutionTimeout)
	sha, _, err := n.sourceControl.ResolveBranchSHA(shaCtx, ports.ResolveBranchSHASpec{
		Owner:  owner,
		Repo:   repoName,
		Branch: payload.OriginHeadBranch,
		Token:  n.githubBotToken,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("resolve origin head branch %q current sha: %w", payload.OriginHeadBranch, err)
	}

	newBranch := sentinelFixBranchName(payload.SentinelFixID)

	createCtx, cancel := context.WithTimeout(ctx, n.timeouts.RepoSHAResolutionTimeout)
	err = n.sourceControl.CreateBranch(createCtx, ports.CreateBranchSpec{
		Owner:  owner,
		Repo:   repoName,
		Branch: newBranch,
		SHA:    sha,
		Token:  n.githubBotToken,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("create branch %q at sha %q: %w", newBranch, sha, err)
	}

	return newBranch, nil
}
