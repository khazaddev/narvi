// This file (outbox_shadow.go) implements §30.8's own epoch discipline
// for the outbox: "stamp the effective egress mode onto every durable
// decision artifact, and suppress if the stamp OR the current flag says
// shadow -- monotone toward suppression, in both directions."
//
// OutboxStore.Create (outbox_store.go) is the single choke point §30.6
// names for this: there is exactly one INSERT INTO outbox in this
// codebase, so the stamp lives here rather than at each of its many
// enqueue call sites, none of which need to change. The resolution
// itself deliberately reuses egressmode.Resolve -- the one authority
// §30.8 names for the live/shadow question -- rather than re-deriving
// its own copy of the "platformShadow OR NOT live_egress_enabled"
// formula: this package already has everything Resolve needs (the
// session's own repo, reached through the SAME already-transaction-
// scoped *sqlcgen.Queries every other method on this store already
// uses) without requiring a caller to compute or pass anything extra.
//
// A row's own repo is derived from sessions.repos (migrations/
// 000018_session_repos.up.sql), reached via the row's own session_id --
// already a mandatory column on every enqueue call, so no signature on
// OutboxStore.Create needs to change. Three outcomes:
//
//   - No session_id at all (a repo-less enqueue -- release_manifest,
//     slack_digest/linear_digest, blob_delete), or a session naming
//     zero/more-than-one repo: the per-repo axis simply does not apply
//     (mirrors internal/app/workflowengine's own identical
//     repoFullNameFromSessionRepos judgment call, lane.go) --
//     egressmode.ResolvePlatform decides using ONLY the deployment-wide
//     switch.
//   - A session naming exactly one repo, resolved cleanly: egressmode.
//     Resolve, the real per-repo formula.
//   - A genuine read error resolving the session itself (never merely
//     "this session has no single repo"): fails closed to shadow
//     directly, logged -- mirrors egressmode.Resolve's own identical
//     "a degraded read can never resolve toward live" posture for its
//     own repo_settings read.
//
// ResolveEffectiveMode (below) is the SAME formula, exported so
// outboxworker.Builder can call it a second time, at delivery, for a
// row born live -- the §30.8 "suppress if the stamp OR the current flag
// says shadow" half enqueue time alone cannot provide (a demotion
// between enqueue and delivery). Create calls it once and freezes the
// result into the row; Builder calls it again, only when the frozen
// stamp says live, to catch exactly that race.
package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/egressmode"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// repoSettingsReader adapts this package's own *sqlcgen.Queries (already
// scoped to whatever pool/transaction this OutboxStore itself is scoped
// to -- see WithTx, outbox_store.go) to egressmode.RepoSettingsReader,
// so egressmode.Resolve's own repo_settings read participates in the
// SAME transaction as the outbox row it is about to stamp, without this
// package needing a second, separately-constructed *RepoSettingsStore.
type repoSettingsReader struct{ q *sqlcgen.Queries }

func (r repoSettingsReader) Get(ctx context.Context, repoFullName string) (sqlcgen.RepoSetting, error) {
	return r.q.GetRepoSettings(ctx, repoFullName)
}

// ResolveEffectiveMode reports whether sessionID's own single repo (if
// it names exactly one) is currently suppressed -- §30.8's own formula,
// evaluated fresh, right now, against whatever repo_settings.
// live_egress_enabled / platformShadow currently say. Called from two
// places for two different reasons: Create below, once, at enqueue time
// (the value it returns there is then frozen onto the row forever); and
// outboxworker.Builder.attempt, again, at delivery time, but ONLY for a
// row whose frozen stamp says live -- the delivery-time "did this repo
// get demoted since enqueue" half of §30.8's own suppress-wins rule.
//
// See this file's own top doc comment for the three-way session/repo
// resolution this shares with Create.
func (s *OutboxStore) ResolveEffectiveMode(ctx context.Context, sessionID pgtype.UUID) bool {
	if !sessionID.Valid {
		return egressmode.ResolvePlatform(s.platformShadow).Suppressed()
	}

	session, err := s.q.GetSession(ctx, sessionID)
	if err != nil {
		// A genuine failure to resolve the session itself (not merely
		// "this session names no single repo") -- mirrors egressmode.
		// Resolve's own "a degraded read can never resolve toward live"
		// posture for its own repo_settings read. pgx.ErrNoRows here
		// would mean a session_id that names no real row at all, which
		// should be unreachable given outbox.session_id's own FK
		// constraint (migrations/000010_outbox.up.sql) -- logged exactly
		// like any other genuine error, never silently treated as "no
		// repo" (which would resolve via the deployment-wide switch
		// alone, the wrong, more permissive path for an anomaly this
		// significant).
		platform.Logger(ctx).Warn("postgres: outbox: resolve session for egress-mode stamp failed -- resolving shadow (fail-closed, §30.8)", "error", err, "session_id", sessionID.String())
		return true
	}

	repoFullName, ok := singleSessionRepoFullName(session.Repos)
	if !ok {
		return egressmode.ResolvePlatform(s.platformShadow).Suppressed()
	}

	return egressmode.Resolve(ctx, egressmode.Deps{
		RepoSettings:   repoSettingsReader{q: s.q},
		PlatformShadow: s.platformShadow,
	}, repoFullName).Suppressed()
}

// singleSessionRepoFullName derives a single unambiguous "owner/repo"
// full name from a session's own raw sessions.repos column (JSONB) --
// pure, no I/O. Mirrors internal/app/workflowengine's own
// repoFullNameFromSessionRepos (lane.go) field for field and judgment
// call for judgment call (that package's own doc comment there explains
// the "exactly one, unambiguous repo, or bail" reasoning in full) --
// duplicated rather than imported, matching this codebase's own
// established convention of a small, private per-package repo-JSON
// helper (internal/app/sessionactor's own reposFromJSON, internal/
// adapters/inbound/httpapi's own sessionRepoFullNames) rather than one
// shared reposession-parsing package no Step has ever introduced.
func singleSessionRepoFullName(rawRepos []byte) (string, bool) {
	if len(rawRepos) == 0 {
		return "", false
	}
	var repos []restdtos.CreateSessionRequestReposElem
	if err := json.Unmarshal(rawRepos, &repos); err != nil || len(repos) != 1 {
		return "", false
	}
	owner, repo, err := reposource.ParseOwnerRepo(repos[0].Url)
	if err != nil {
		return "", false
	}
	return owner + "/" + repo, true
}

// MarkDeliveredToLedger records §30.6/§30.8's own terminal mark: this
// row's own effective egress mode was shadow at the moment
// outboxworker.Builder.attempt would otherwise have called
// notifier.Deliver, so it delivered the row into the suppression ledger
// instead of the world. See MarkOutboxEntryDeliveredToLedger's own
// generated doc comment for the exact column-level effect and why this
// reuses the SAME 'delivered' status MarkDelivered does. Returns
// pgx.ErrNoRows if id's row is no longer 'pending', mirroring
// MarkDelivered's own identical guard.
func (s *OutboxStore) MarkDeliveredToLedger(ctx context.Context, id pgtype.UUID) (sqlcgen.Outbox, error) {
	return s.q.MarkOutboxEntryDeliveredToLedger(ctx, id)
}
