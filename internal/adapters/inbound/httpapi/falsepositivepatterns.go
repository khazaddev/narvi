// This file implements §22's own §22.4 lifecycle surface (audit view
// + retire) for review_false_positive_patterns -- shipped in the SAME
// Step as the capture command (§22.2, internal/adapters/inbound/github's
// own dispatch-before-router handler), never a deferred follow-up: "a
// learned-pattern table with no retirement path only ever grows,
// accumulating stale or wrong patterns with no mechanism to review or
// remove them" (§22.4).
//
// Both routes are gated by authz.ActionManageFalsePositivePatterns
// (§13.3 row 5, maintainer+, no member own/joined carve-out -- a taught
// pattern is repo-scoped, never session-scoped) and mounted behind
// auth.Middleware, alongside every other browser-facing REST route in
// this package (this is an admin/maintainer reviewing or retiring a
// pattern, not the sandbox agent calling a tool). §22.5 names the actual
// Settings UI consuming these two endpoints as §14.4 (Phase 7) -- the
// underlying capability ships here, now, exactly like review_findings'
// own rebut/apply-suggestion endpoints predated any "finding cards" UI
// (§12.2 item 2, also Phase 7).

package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// defaultFalsePositivePatternPageSize/maxFalsePositivePatternPageSize
// mirror ListAuditLog's own identical default/max query-param pagination
// precedent (members.go) -- this audit view is bounded from day one
// (ListFalsePositivePatterns' own generated doc comment, reviewfalsepositivepatterns.sql),
// never an unbounded "list everything, ever" read.
const (
	defaultFalsePositivePatternPageSize = 50
	maxFalsePositivePatternPageSize     = 200
)

// falsePositivePatternToWire converts one sqlcgen.ReviewFalsePositivePattern
// row into its REST wire shape.
func falsePositivePatternToWire(p sqlcgen.ReviewFalsePositivePattern) restdtos.FalsePositivePattern {
	out := restdtos.FalsePositivePattern{
		Id:           p.ID.String(),
		RepoFullName: p.RepoFullName,
		Reason:       p.Reason,
		CreatedAt:    p.CreatedAt.Time,
		HitCount:     int(p.HitCount),
	}
	if p.LastHitAt.Valid {
		t := p.LastHitAt.Time
		out.LastHitAt = &t
	}
	if p.RetiredAt.Valid {
		t := p.RetiredAt.Time
		out.RetiredAt = &t
	}
	return out
}

// ListFalsePositivePatterns backs GET
// /api/repos/{owner}/{repo}/false-positive-patterns (§22.4's own audit
// view): 403 if the caller fails authz.ActionManageFalsePositivePatterns;
// 200 with restdtos.ListFalsePositivePatternsResponse otherwise, EVERY
// pattern for this repo (active or retired), newest-first, bounded by an
// optional ?limit= query param (mirrors ListAuditLog's own identical
// clamp-to-[1,max] convention, members.go).
func ListFalsePositivePatterns(patterns *postgres.FalsePositivePatternStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageFalsePositivePatterns, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		limit := int32(defaultFalsePositivePatternPageSize)
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= maxFalsePositivePatternPageSize {
				limit = int32(parsed)
			}
		}

		rows, err := patterns.List(ctx, repoFullName, limit)
		if err != nil {
			logger.Error("httpapi: list false-positive patterns failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		out := make([]restdtos.FalsePositivePattern, len(rows))
		for i, row := range rows {
			out[i] = falsePositivePatternToWire(row)
		}

		writeJSON(w, http.StatusOK, restdtos.ListFalsePositivePatternsResponse{Patterns: out})
	}
}

// RetireFalsePositivePattern backs POST
// /api/repos/{owner}/{repo}/false-positive-patterns/{patternID}/retire
// (§22.4): 400 for a malformed patternID; 403 if the caller fails authz.
// ActionManageFalsePositivePatterns; 404 if no pattern with this id
// exists in this repo at all; 409 if it exists but was ALREADY retired
// (RetireFalsePositivePattern's own guarded UPDATE, WHERE retired_at IS
// NULL -- CLAUDE.md/§11's "guarded UPDATE... for cross-writer
// transitions" rule); 200 with the resulting restdtos.
// FalsePositivePattern otherwise. Writes a REAL audit_log row (actor_
// user_id set to the authenticated caller), mirroring RebutReviewFinding's
// own identical "a human-attributed action" precedent (reviewfindings.go).
func RetireFalsePositivePattern(patterns *postgres.FalsePositivePatternStore, auditLog *postgres.AuditLogStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageFalsePositivePatterns, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		var patternID pgtype.UUID
		if err := patternID.Scan(chi.URLParam(r, "patternID")); err != nil {
			writeError(w, http.StatusBadRequest, "malformed pattern id")
			return
		}

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		updated, err := patterns.Retire(ctx, patternID, actorUserID, repoFullName)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Guarded UPDATE's own WHERE retired_at IS NULL clause
				// rejected this write -- distinguish "never existed in this
				// repo" (404) from "exists in this repo but already
				// retired" (409) with a follow-up read, mirroring
				// FalsePositivePatternStore.Retire's own doc comment. Both
				// Retire and this follow-up Get are scoped to repoFullName
				// (audit fix: previously id alone, letting a pattern
				// belonging to a DIFFERENT repo be retrieved/retired
				// through the wrong repo's own URL) -- a cross-repo
				// mismatch now falls into this SAME "never existed" 404
				// path, matching this handler's own doc comment's already-
				// documented contract.
				_, getErr := patterns.Get(ctx, patternID, repoFullName)
				if getErr != nil {
					if errors.Is(getErr, pgx.ErrNoRows) {
						writeError(w, http.StatusNotFound, "no false-positive pattern with this id")
						return
					}
					logger.Error("httpapi: get false-positive pattern after failed retire failed", "error", getErr)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				writeError(w, http.StatusConflict, "this pattern is already retired")
				return
			}
			logger.Error("httpapi: retire false-positive pattern failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// repo_full_name comes from the RESOLVED row, never the URL --
		// they are now guaranteed equal (Retire's own query is scoped to
		// repoFullName), but taking it from the row is the more honest
		// source of truth for what was actually mutated.
		if err := recordAuditLog(ctx, auditLog, actorUserID, "false_positive_pattern.retire", "false_positive_pattern", patternID.String(), map[string]any{
			"repo_full_name": updated.RepoFullName,
		}); err != nil {
			logger.Error("httpapi: record false_positive_pattern.retire audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, falsePositivePatternToWire(updated))
	}
}
