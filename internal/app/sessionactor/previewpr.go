// This file (previewpr.go) implements §4.1's own ("RWX provider +
// previews", §4.1.2 point 1) PR preview link enqueue: for each pushed
// repo whose per-repo RWX preview setting ({dispatchKey, endpointTemplate,
// orgSlug}) is present, one small fresh transact writes a "preview"-typed
// artifact row plus the two new outbox rows (rwx_preview_dispatch,
// github_preview_link) — mirroring pushpr.go's own recordPRArtifact
// established "a real network call already happened outside any
// transaction; a separate, small transact now records its outcome" shape
// exactly.
//
// Called from createPRBestEffort ONLY — the ONE enqueue point per §4.1.2
// point 1 — immediately after a repo's own PR has been created (or
// recovered, CreatePR's own idempotency) and its "pr"-typed artifact
// recorded. NEVER called from createSentinelFixPRBestEffort: a
// sentinel-auto-fix session has no human creator and is already a
// dedicated, narrower code path (see that function's own doc comment) —
// previewing an internal test/doc-only follow-up PR is not this Step's
// own job.
//
// A repo whose settings row is absent, or whose three preview fields are
// only PARTIALLY configured, is treated identically: previews OFF for
// that repo (§24.5's own "if the setting cannot be read... treated as
// OFF" precedent for a comparable per-repo policy flag) — logged only
// when the read itself genuinely failed (a real, unexpected DB error),
// never for the ordinary "no row yet"/"not configured" case.
//
// Deliberately NOT idempotency-guarded the way recordPRArtifact is: each
// push carries a genuinely NEW sha (pushed.Sha), so a fresh preview
// artifact + outbox pair per push is the CORRECT behavior (§4.1.2 point
// 3: "each push posts at the new head"), not a duplicate to suppress.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// pushedShaPattern matches a full, lowercase, 40-character hex commit sha
// -- the only shape a real `git push` ever produces pushed.Sha
// (sandboxws.PushCompleteReposElem.Sha) in. The wire schema itself leaves
// the field an unconstrained string (contracts/sandbox-ws/v1/events.schema.json's
// own "sha": {"type": "string"}), so a buggy or compromised sandbox
// process could send anything at all. Enforced at the ONE point both
// outbound preview payloads are built from it (security fix,
// enqueuePreviewBestEffort below): PreviewLinkPayload.SHA is posted to
// GitHub's CreateCommitStatus by the PLATFORM BOT token
// (githubapi/previewlinknotifier.go) -- an unvalidated value there is
// attacker-controlled input reaching a write call authenticated with a
// credential the pusher never had -- and PreviewDispatchPayload.Ref/HeadSHA
// become the literal git ref RWX itself builds (rwx/previewnotifier.go).
// Neither downstream call validates its own input, so this is the only
// gate; a mismatch is treated exactly like an unconfigured preview setting
// (best-effort, warn-logged, never fails push handling).
var pushedShaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// previewSettings is the three-field per-repo RWX preview configuration
// (§4.1.2 point 1), extracted from a repo_settings row.
type previewSettings struct {
	dispatchKey      string
	endpointTemplate string
	orgSlug          string
}

// readPreviewSettings reads repoFullName's repo_settings row and reports
// whether its three RWX preview fields are all present (ok=true) — ANY
// error reading the row (including pgx.ErrNoRows, the ordinary "never
// configured" case) or ANY of the three fields missing/empty collapses to
// (previewSettings{}, false), matching this table's own established
// fail-closed-on-missing-row-or-transient-read-error precedent
// (migrations/000044_repo_settings.up.sql's own doc comment: "callers
// read this table with a fail-closed default on a missing row or a
// transient read error"). A genuine, unexpected DB error (never
// pgx.ErrNoRows) is additionally logged, since that case is worth an
// operator's attention; an ordinary "not configured yet" is not.
func (a *Actor) readPreviewSettings(ctx context.Context, repoFullName string) (previewSettings, bool) {
	row, err := a.stores.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Warn("sessionactor: read repo settings for preview dispatch failed; treating previews as disabled for this repo",
				"repo", repoFullName, "error", err)
		}
		return previewSettings{}, false
	}

	if row.RwxPreviewDispatchKey == nil || *row.RwxPreviewDispatchKey == "" ||
		row.RwxPreviewEndpointTemplate == nil || *row.RwxPreviewEndpointTemplate == "" ||
		row.RwxPreviewOrgSlug == nil || *row.RwxPreviewOrgSlug == "" {
		return previewSettings{}, false
	}

	return previewSettings{
		dispatchKey:      *row.RwxPreviewDispatchKey,
		endpointTemplate: *row.RwxPreviewEndpointTemplate,
		orgSlug:          *row.RwxPreviewOrgSlug,
	}, true
}

// enqueuePreviewBestEffort implements this file's own job: called by
// createPRBestEffort immediately after repoName's own PR has been
// created/recovered, for owner/repoName's own pushed commit (pushed.Sha)
// and the resulting PRRef ref. A no-op (logged only on a genuine read
// error — readPreviewSettings' own doc comment) when the repo's own
// preview setting is absent or only partially configured.
func (a *Actor) enqueuePreviewBestEffort(ctx context.Context, owner, repoName string, pushed sandboxws.PushCompleteReposElem, ref ports.PRRef) {
	repoFullName := owner + "/" + repoName
	settings, ok := a.readPreviewSettings(ctx, repoFullName)
	if !ok {
		return
	}

	// Security fix: validate pushed.Sha at this boundary, before it can
	// reach either outbound payload below -- see pushedShaPattern's own
	// doc comment. Best-effort posture, matching this whole function's
	// established "log and skip THIS repo's preview" discipline: never
	// fails push handling itself.
	if !pushedShaPattern.MatchString(pushed.Sha) {
		a.logger.Warn("sessionactor: push_complete carried a malformed sha; skipping preview enqueue for this repo",
			"repo", repoFullName, "sha", pushed.Sha)
		return
	}

	friendlyURL, err := rwx.FriendlyPreviewURL(settings.endpointTemplate, ref.Number, settings.orgSlug)
	if err != nil {
		a.logger.Warn("sessionactor: rendered rwx preview url failed host-pin validation; skipping preview enqueue for this repo",
			"repo", repoFullName, "error", err)
		return
	}

	dispatchPayload, err := json.Marshal(rwx.PreviewDispatchPayload{
		DispatchKey: settings.dispatchKey,
		Ref:         pushed.Sha,
		PRNumber:    ref.Number,
		HeadSHA:     pushed.Sha,
		SessionID:   a.sessionID.String(),
	})
	if err != nil {
		a.logger.Error("sessionactor: marshal rwx preview dispatch payload failed", "repo", repoFullName, "error", err)
		return
	}

	linkPayload, err := json.Marshal(githubapi.PreviewLinkPayload{
		Owner:       owner,
		Repo:        repoName,
		SHA:         pushed.Sha,
		TargetURL:   friendlyURL,
		Description: "Preview deployed via RWX -- ephemeral, live only while RWX continues to serve it.",
	})
	if err != nil {
		a.logger.Error("sessionactor: marshal github preview link payload failed", "repo", repoFullName, "error", err)
		return
	}

	artifactMetadata, err := json.Marshal(map[string]any{"repo": repoFullName, "pr_number": ref.Number, "sha": pushed.Sha})
	if err != nil {
		a.logger.Error("sessionactor: marshal preview artifact metadata failed", "repo", repoFullName, "error", err)
		return
	}

	// Correlation ID propagation, mirroring outboxenqueue.go's own
	// identical convention: carries the enclosing request/webhook's own
	// correlation id when present, else null -- no id is ever invented
	// here.
	var correlationID *string
	if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
		correlationID = &id
	}

	if err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := a.stores.artifact.WithTx(tx).Create(ctx, sqlcgen.CreateArtifactParams{
			SessionID: a.sessionID,
			Type:      sqlcgen.ArtifactTypePreview,
			Url:       friendlyURL,
			Metadata:  artifactMetadata,
		}); err != nil {
			return fmt.Errorf("sessionactor: insert preview artifact: %w", err)
		}

		if _, err := a.stores.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID:     a.sessionID,
			Kind:          string(ports.NotificationKindRWXPreviewDispatch),
			Payload:       dispatchPayload,
			CorrelationID: correlationID,
		}); err != nil {
			return fmt.Errorf("sessionactor: insert rwx_preview_dispatch outbox entry: %w", err)
		}

		if _, err := a.stores.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID:     a.sessionID,
			Kind:          string(ports.NotificationKindGitHubPreviewLink),
			Payload:       linkPayload,
			CorrelationID: correlationID,
		}); err != nil {
			return fmt.Errorf("sessionactor: insert github_preview_link outbox entry: %w", err)
		}

		return nil
	}); err != nil {
		a.logger.Error("sessionactor: enqueue preview artifact/outbox rows failed", "repo", repoFullName, "error", err)
	}
}
