// This file (shadowledger.go) implements the shadow-operator surface's
// own REST routes (§30.6, §30.8, §30.9):
//
//   - GET  /api/repos/{owner}/{repo}/shadow-ledger          (read model)
//   - POST /api/repos/{owner}/{repo}/shadow-ledger/activate (graduation)
//
// Both are gated by a NEW admin-only pair, authz.ActionViewShadowLedger/
// ActionActivateShadowLedger -- see that action's own doc comment
// (internal/domain/authz/action.go) for why no §13.3 table row names
// either explicitly: this ledger holds customer source code at rest in
// full, and its own retention/PII policy (§30.9) is still open.
//
// All the actual work -- the §30.6 UNION read, the §30.8 promotion
// fence, the §30.8 shadow-era quarantine refusal -- lives in
// internal/app/shadowoperator; this file is translation only: chi params
// to a repoFullName (resolveKnownRepo, this SAME package's established
// convention), a Summary to its restdtos.ShadowLedgerSummary wire shape,
// and a domain error to the right HTTP status.

package httpapi

import (
	"errors"
	"net/http"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/shadowoperator"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// GetShadowLedger backs GET /api/repos/{owner}/{repo}/shadow-ledger: 403
// if the caller fails authz.ActionViewShadowLedger (admin only); 200 with
// restdtos.ShadowLedgerSummary otherwise. A repository with no suppressed
// activity at all (or none yet recorded, e.g. a live repo) renders as an
// EMPTY, not-computed summary -- never a 404, mirroring GetRepoSettings'
// own "no row yet is not an error condition" precedent immediately above
// this file's own package.
func GetShadowLedger(ledger *postgres.ShadowSCMWriteStore, reads *postgres.ShadowOperatorReadStore, repoSettings *postgres.RepoSettingsStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionViewShadowLedger, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
		if err != nil {
			logger.Error("httpapi: build shadow ledger summary failed", "error", err, "repo", repoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, shadowLedgerSummaryToDTO(summary))
	}
}

// PostActivateShadowLedger backs POST
// /api/repos/{owner}/{repo}/shadow-ledger/activate: 403 if the caller
// fails authz.ActionActivateShadowLedger (admin only); 409 if
// internal/app/shadowoperator.Activate refuses because unhandled
// shadow-era rows remain for this repository (§30.8's own quarantine);
// 200 with the freshly-rebuilt restdtos.ShadowLedgerSummary otherwise --
// the caller sees the promoted state immediately, with no second GET
// required.
func PostActivateShadowLedger(ledger *postgres.ShadowSCMWriteStore, reads *postgres.ShadowOperatorReadStore, repoSettings *postgres.RepoSettingsStore, auditLog *postgres.AuditLogStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionActivateShadowLedger, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		if _, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, actorUserID); err != nil {
			var unhandled *shadowoperator.ErrUnhandledShadowEraRows
			if errors.As(err, &unhandled) {
				writeError(w, http.StatusConflict, unhandled.Error())
				return
			}
			logger.Error("httpapi: activate shadow ledger failed", "error", err, "repo", repoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// A fresh BuildSummary rather than hand-assembling a response from
		// Activate's own return value: the caller's whole reason to POST
		// here was to see the repository's shadow ledger, and this is the
		// SAME read every GET uses, so the two can never drift into two
		// slightly different response shapes for the same state.
		summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
		if err != nil {
			logger.Error("httpapi: rebuild shadow ledger summary after activate failed", "error", err, "repo", repoFullName)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, shadowLedgerSummaryToDTO(summary))
	}
}

// shadowLedgerSummaryToDTO maps shadowoperator.Summary onto its wire
// shape -- a pure translation, no business logic (that already ran
// inside BuildSummary/Activate).
func shadowLedgerSummaryToDTO(summary shadowoperator.Summary) restdtos.ShadowLedgerSummary {
	categories := make([]restdtos.ShadowLedgerCategory, 0, len(summary.Categories))
	for _, c := range summary.Categories {
		categories = append(categories, restdtos.ShadowLedgerCategory{Label: c.Label, Count: c.Count})
	}

	entries := make([]restdtos.ShadowLedgerEntry, 0, len(summary.Entries))
	for _, e := range summary.Entries {
		entries = append(entries, restdtos.ShadowLedgerEntry{
			Source:    restdtos.ShadowLedgerEntrySource(e.Source),
			Operation: e.Operation,
			Category:  e.Category,
			Target:    optionalStringDTO(e.Target),
			SessionId: (restdtos.ShadowLedgerEntrySessionId)(optionalStringDTO(e.SessionID)),
			CreatedAt: e.CreatedAt,
		})
	}

	var llmSpendUsd restdtos.ShadowLedgerSummaryLlmSpendUsd
	if summary.LLMSpendComputed {
		v := summary.LLMSpendUsd
		llmSpendUsd = &v
	}

	return restdtos.ShadowLedgerSummary{
		RepoFullName:          summary.RepoFullName,
		LiveEgressEnabled:     summary.LiveEgressEnabled,
		LiveEgressPromotedAt:  restdtos.ShadowLedgerSummaryLiveEgressPromotedAt(summary.LiveEgressPromotedAt),
		PendingShadowEraCount: summary.PendingShadowEraCount,
		Categories:            categories,
		TotalCount:            summary.TotalCount,
		LlmSpendComputed:      summary.LLMSpendComputed,
		LlmSpendUsd:           llmSpendUsd,
		Entries:               entries,
	}
}

// optionalStringDTO maps "" to a nil *string -- Entry.Target/SessionID
// both use "" as their own "absent" value (readmodel.go's own
// stringOrEmpty/uuidOrEmpty), which must render as JSON null, never the
// empty string, on the wire (ShadowLedgerEntry.target/sessionId are both
// nullable, not merely optional).
func optionalStringDTO(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
