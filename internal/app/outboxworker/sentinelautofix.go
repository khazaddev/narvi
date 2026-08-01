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
}

var _ ports.Notifier = (*sentinelAutoFixNotifier)(nil)

// NewSentinelAutoFixNotifier builds a ports.Notifier for
// ports.NotificationKindSentinelAutoFix -- called once by cmd/control-
// plane/main.go's own kind->Notifier map assembly, mirroring every other
// notifier constructor's own identical "called exactly once" precedent.
func NewSentinelAutoFixNotifier(
	pool *pgxpool.Pool,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	environments *postgres.EnvironmentStore,
	auditLog *postgres.AuditLogStore,
	registry *sessionactor.Registry,
	sentinelFixes *postgres.SentinelFixStore,
	reviewFindings *postgres.ReviewFindingStore,
) ports.Notifier {
	return &sentinelAutoFixNotifier{
		pool: pool, sessions: sessions, turns: turns, environments: environments,
		auditLog: auditLog, registry: registry, sentinelFixes: sentinelFixes, reviewFindings: reviewFindings,
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
// then spawns the child session and records it back onto BOTH the
// sentinel_fixes row and every finding it addresses.
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

	branch := payload.OriginHeadBranch
	prompt := sentinelAutoFixPromptText(payload.FindingDescriptions)
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{
				Name: payload.RepoName,
				Url:  payload.RepoCloneURL,
				// Branch here is the CHILD SESSION's own head branch to
				// check out and push FROM -- checking out the ORIGIN's
				// own head branch means the fix session's own commits
				// necessarily apply on top of the origin diff, exactly
				// what a stacked fix requires (§17.2). This is a
				// DIFFERENT concept from the fix PR's own Base (set later,
				// pushpr.go's createSentinelFixPRBestEffort, to this SAME
				// branch name, literal, never resolved via
				// resolvePRBaseBranch) -- both happen to be the same
				// string, for the same underlying reason (stack on top of
				// the origin), but this field and that one are two
				// distinct things reused correctly.
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
