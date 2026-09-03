// This file (workflowbindings.go) implements §25.10's own binding
// routes: GET /api/workflow-bindings (every (lane, repo) binding) and
// PUT /api/workflow-bindings (bind one (lane, repo-or-global) pair to a
// definition).
//
// GET is gated by authz.ActionManageWorkflowDefinitions (maintainer+,
// §25.11) -- the SAME read gate as the definitions list (workflowdefinitions.go):
// an editor needs to see current bindings to know which definitions are
// safe to edit (the "unbound draft" check that file's own top doc comment
// describes). PUT is gated by authz.ActionActivateWorkflowBinding
// (admin-only, §25.11) -- the SAME action for both the global and a
// repo-scoped binding; activation is the ONE action that changes what
// actually drives production dispatch (§25.6), never a per-draft
// authoring step.
//
// # The two-partial-unique-index upsert
//
// workflow_bindings carries TWO partial unique indexes --
// workflow_bindings_repo_uniq ON (lane, repo_full_name) WHERE
// repo_full_name IS NOT NULL, and workflow_bindings_global_uniq ON (lane)
// WHERE repo_full_name IS NULL (migration 000057) -- because a plain
// UNIQUE never matches on NULL, so "ON CONFLICT (lane, repo_full_name)"
// alone would never match the global row and a naive single upsert would
// silently INSERT A SECOND global binding for a lane. This handler picks
// between postgres.WorkflowStore's own two scope-specific upserts
// (UpsertGlobalBinding/UpsertRepoBinding, each naming its own arbiter
// partial index) based on req.RepoFullName being nil or not -- never a
// single parameterized statement, mirroring
// postgres.OpenCodeConfigStore's own identical two-upsert precedent
// (queries/opencodeconfigs.sql).

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// workflowBindingToDTO converts one workflow_bindings row to its own
// wire shape -- shared by ListWorkflowBindings and PutWorkflowBinding so
// neither independently drifts from the other's own field-by-field
// rendering.
func workflowBindingToDTO(b sqlcgen.WorkflowBinding) restdtos.WorkflowBinding {
	return restdtos.WorkflowBinding{
		Id:                   b.ID.String(),
		Lane:                 restdtos.WorkflowBindingLane(b.Lane),
		RepoFullName:         restdtos.WorkflowBindingRepoFullName(b.RepoFullName),
		WorkflowDefinitionId: b.WorkflowDefinitionID.String(),
		DefinitionVersion:    int(b.DefinitionVersion),
		CreatedAt:            b.CreatedAt.Time,
		UpdatedAt:            b.UpdatedAt.Time,
	}
}

// ListWorkflowBindings backs GET /api/workflow-bindings (§25.10): every
// binding, the 3 seeded global rows always included.
func ListWorkflowBindings(workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}

		rows, err := workflows.ListBindings(ctx)
		if err != nil {
			logger.Error("httpapi: list workflow bindings failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		out := make([]restdtos.WorkflowBinding, 0, len(rows))
		for _, row := range rows {
			out = append(out, workflowBindingToDTO(row))
		}

		writeJSON(w, http.StatusOK, restdtos.ListWorkflowBindingsResponse{Bindings: out})
	}
}

// PutWorkflowBinding backs PUT /api/workflow-bindings (§25.10/§25.11):
// binds (lane, repoFullName) to workflowDefinitionId at that definition's
// CURRENT version -- definitionVersion is pinned server-side from the
// target definition's own version column at write time, never a
// client-supplied value. repoFullName null targets the global binding
// for lane; non-null targets that repo's own override. Admin only.
func PutWorkflowBinding(pool *pgxpool.Pool, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionActivateWorkflowBinding, authz.Resource{}) {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.PutWorkflowBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}

		var definitionID pgtype.UUID
		if err := definitionID.Scan(req.WorkflowDefinitionId); err != nil {
			writeError(w, http.StatusBadRequest, "malformed workflowDefinitionId")
			return
		}

		// This handler ran entirely outside a transaction, each store call
		// autocommitting on its own. That made §25.11's bound-definition
		// refusal defeatable from THIS side: a PUT on the definition could
		// check for bindings, find none, and still be mid-flight when this
		// upsert committed one -- so the edit landed on a definition that was
		// bound by the time it committed. Both sides now take the same row
		// lock on workflow_definitions inside a transaction, so activation and
		// editing serialise instead of interleaving.
		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin put-workflow-binding tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		txWorkflows := workflows.WithTx(tx)

		definition, err := txWorkflows.LockDefinitionForUpdate(ctx, definitionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "workflow definition not found")
				return
			}
			logger.Error("httpapi: lock workflow definition for binding failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if string(definition.Lane) != string(req.Lane) {
			// The definition FK (workflow_bindings_definition_lane_fk,
			// migration 000057) would refuse this at the DB level anyway
			// -- caught here first for a clean 400 naming the mismatch,
			// rather than a raw constraint-violation 500 (§25.10's own
			// "validate first" discipline).
			writeError(w, http.StatusBadRequest, "workflowDefinitionId's own lane does not match the requested binding lane")
			return
		}

		var binding sqlcgen.WorkflowBinding
		if req.RepoFullName == nil {
			binding, err = txWorkflows.UpsertGlobalBinding(ctx, string(req.Lane), definitionID, definition.Version)
			if err != nil {
				logger.Error("httpapi: upsert global workflow binding failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		} else {
			repoFullName := *req.RepoFullName
			if repoFullName == "" {
				writeError(w, http.StatusBadRequest, "repoFullName must be non-empty, or null for the global binding")
				return
			}
			binding, err = txWorkflows.UpsertRepoBinding(ctx, string(req.Lane), repoFullName, definitionID, definition.Version)
			if err != nil {
				logger.Error("httpapi: upsert repo workflow binding failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit put-workflow-binding tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		logger.Info("httpapi: put workflow binding", "lane", string(binding.Lane), "definition_id", binding.WorkflowDefinitionID.String())
		writeJSON(w, http.StatusOK, workflowBindingToDTO(binding))
	}
}
