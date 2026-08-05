//go:build integration

// Integration proof for migrations/000057_workflows.up.sql (Step 54,
// "domain/workflow + loopguard + schema", §25.4/§25.8): the three
// built-in workflow definitions, their steps and edge, and the three
// global bindings come out of migrate-up seeded and well-formed in
// exactly §25.8's zero-config shapes, and the schema's structural
// invariants (one built-in per lane, one global binding per lane,
// binding-lane/definition-lane agreement, cross-definition edges
// unrepresentable, one deterministic edge per (step, outcome), one
// running run per session, one live attempt per run) hold at the DB
// level, not just in application code. Deliberately raw SQL throughout:
// NO store layer exists for these tables yet -- Step 54 is dark by
// design (schema/domain/contracts/RBAC only), and Steps 55-56 own the
// first real read/write paths.
package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The fixed system-row ids migration 000057 seeds (see its own header
// comment for why these are constants rather than gen_random_uuid()).
const (
	builtInReviewDefID  = "00000000-0000-4000-8000-000000000001"
	builtInRequestDefID = "00000000-0000-4000-8000-000000000002"
	builtInPlanDefID    = "00000000-0000-4000-8000-000000000003"
	builtInPlanStep1ID  = "00000000-0000-4000-8000-000000000031"
	builtInPlanStep2ID  = "00000000-0000-4000-8000-000000000032"
)

// expectPgErrCode asserts err is a *pgconn.PgError with the given
// SQLSTATE and constraint -- the same idiom
// postgres_integration_test.go's own one-processing-turn assertion uses.
func expectPgErrCode(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("statement succeeded, want SQLSTATE %s on constraint %s", code, constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v (%T), want a *pgconn.PgError", err, err)
	}
	if pgErr.Code != code {
		t.Fatalf("error code = %q (constraint %q), want %q", pgErr.Code, pgErr.ConstraintName, code)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}

// TestWorkflowSeed_BuiltInDefinitions proves the three system templates
// exist exactly as §25.8 shapes them: review/request are single
// passthrough steps (ModelID nil so turns.model_id/sessions.
// build_model_id inherit exactly as today -- Step 55's zero-config
// proof), plan is the 2-step approve/build shape with HITL after step 1
// and the ONE explicit needs_fix self-loop edge (the revise
// re-execution loop).
func TestWorkflowSeed_BuiltInDefinitions(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_definitions`).Scan(&total); err != nil {
		t.Fatalf("count workflow_definitions: %v", err)
	}
	if total != 3 {
		t.Fatalf("workflow_definitions count = %d, want exactly the 3 seeded built-ins", total)
	}

	for lane, defID := range map[string]string{
		"review":  builtInReviewDefID,
		"request": builtInRequestDefID,
		"plan":    builtInPlanDefID,
	} {
		var (
			name      string
			isBuiltIn bool
			version   int
		)
		err := pool.QueryRow(ctx,
			`SELECT name, is_built_in, version FROM workflow_definitions WHERE id = $1 AND lane = $2`,
			defID, lane).Scan(&name, &isBuiltIn, &version)
		if err != nil {
			t.Fatalf("seeded %s definition %s: %v", lane, defID, err)
		}
		if name != lane || !isBuiltIn || version != 1 {
			t.Errorf("%s definition = (name %q, is_built_in %v, version %d), want (%q, true, 1)", lane, name, isBuiltIn, version, lane)
		}
	}

	// review/request: exactly one passthrough step each, no edges.
	for _, defID := range []string{builtInReviewDefID, builtInRequestDefID} {
		var (
			stepCount, edgeCount int
			order                int
			kind, prompt         string
			modelID              *string
			scope, continuity    string
			before, after        bool
		)
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM workflow_step_definitions WHERE workflow_definition_id = $1`, defID).Scan(&stepCount); err != nil {
			t.Fatalf("count steps for %s: %v", defID, err)
		}
		if stepCount != 1 {
			t.Fatalf("definition %s step count = %d, want 1 (single passthrough step, §25.8)", defID, stepCount)
		}
		err := pool.QueryRow(ctx, `
			SELECT step_order, kind, model_id, prompt_template, execution_scope, conversation_continuity, hitl_before, hitl_after
			FROM workflow_step_definitions WHERE workflow_definition_id = $1`, defID).
			Scan(&order, &kind, &modelID, &prompt, &scope, &continuity, &before, &after)
		if err != nil {
			t.Fatalf("read step for %s: %v", defID, err)
		}
		if order != 1 || kind != "agent" || modelID != nil || prompt != "{{prompt}}" ||
			scope != "same_session" || continuity != "continue" || before || after {
			t.Errorf("definition %s step = (order %d, kind %q, model %v, prompt %q, scope %q, continuity %q, hitl %v/%v), want the §25.8 passthrough shape",
				defID, order, kind, modelID, prompt, scope, continuity, before, after)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM workflow_edges WHERE workflow_definition_id = $1`, defID).Scan(&edgeCount); err != nil {
			t.Fatalf("count edges for %s: %v", defID, err)
		}
		if edgeCount != 0 {
			t.Errorf("definition %s edge count = %d, want 0 (defaults only)", defID, edgeCount)
		}
	}

	// plan: two ordered steps, HITL after step 1 only, and exactly the
	// needs_fix self-loop edge on step 1.
	rows, err := pool.Query(ctx, `
		SELECT id, step_order, model_id, prompt_template, hitl_before, hitl_after
		FROM workflow_step_definitions WHERE workflow_definition_id = $1 ORDER BY step_order`, builtInPlanDefID)
	if err != nil {
		t.Fatalf("read plan steps: %v", err)
	}
	type stepRow struct {
		id            string
		order         int
		modelID       *string
		prompt        string
		before, after bool
	}
	var steps []stepRow
	for rows.Next() {
		var s stepRow
		if err := rows.Scan(&s.id, &s.order, &s.modelID, &s.prompt, &s.before, &s.after); err != nil {
			rows.Close()
			t.Fatalf("scan plan step: %v", err)
		}
		steps = append(steps, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("plan step count = %d, want 2 (plan -> build, §25.8)", len(steps))
	}
	if steps[0].id != builtInPlanStep1ID || steps[0].order != 1 || steps[0].modelID != nil ||
		steps[0].prompt != "{{prompt}}" || steps[0].before || !steps[0].after {
		t.Errorf("plan step 1 = %+v, want (id %s, order 1, model nil, passthrough prompt, hitl_after only)", steps[0], builtInPlanStep1ID)
	}
	if steps[1].id != builtInPlanStep2ID || steps[1].order != 2 || steps[1].modelID != nil ||
		steps[1].prompt != "{{prompt}}" || steps[1].before || steps[1].after {
		t.Errorf("plan step 2 = %+v, want (id %s, order 2, model nil, passthrough prompt, no HITL)", steps[1], builtInPlanStep2ID)
	}

	var from, to, onStatus string
	if err := pool.QueryRow(ctx, `
		SELECT from_step_id, to_step_id, on_status FROM workflow_edges WHERE workflow_definition_id = $1`, builtInPlanDefID).
		Scan(&from, &to, &onStatus); err != nil {
		t.Fatalf("read plan edge (want exactly one): %v", err)
	}
	if from != builtInPlanStep1ID || to != builtInPlanStep1ID || onStatus != "needs_fix" {
		t.Errorf("plan edge = (%s -> %s on %s), want the needs_fix self-loop on step 1 (§25.8's revise loop)", from, to, onStatus)
	}
}

// TestWorkflowSeed_GlobalBindings proves §25.4's "this row is never
// absent" guarantee holds from migrate-up: exactly one global
// (repo_full_name IS NULL) binding per lane, each pointing at that
// lane's own built-in definition at version 1 -- so binding resolution
// is always a two-step lookup with a guaranteed second step, never an
// "absent row -> default" branch.
func TestWorkflowSeed_GlobalBindings(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_bindings`).Scan(&total); err != nil {
		t.Fatalf("count workflow_bindings: %v", err)
	}
	if total != 3 {
		t.Fatalf("workflow_bindings count = %d, want exactly the 3 seeded global rows", total)
	}

	for lane, wantDefID := range map[string]string{
		"review":  builtInReviewDefID,
		"request": builtInRequestDefID,
		"plan":    builtInPlanDefID,
	} {
		var (
			defID   string
			version int
		)
		err := pool.QueryRow(ctx, `
			SELECT workflow_definition_id, definition_version
			FROM workflow_bindings WHERE lane = $1 AND repo_full_name IS NULL`, lane).
			Scan(&defID, &version)
		if err != nil {
			t.Fatalf("global %s binding (must never be absent, §25.4): %v", lane, err)
		}
		if defID != wantDefID || version != 1 {
			t.Errorf("global %s binding = (%s, v%d), want (%s, v1)", lane, defID, version, wantDefID)
		}
	}
}

// TestWorkflowSchema_StructuralInvariants exercises the
// constraint-over-convention guarantees a caller cannot violate even
// with raw SQL: exactly one built-in per lane, exactly one global
// binding per lane (a repo override remains legal), binding lane must
// equal the bound definition's lane, an edge cannot cross definitions,
// and (from step, outcome) routes to at most one target.
func TestWorkflowSchema_StructuralInvariants(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	t.Run("second built-in per lane rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO workflow_definitions (lane, name, is_built_in) VALUES ('review', 'review-2', true)`)
		expectPgErrCode(t, err, "23505", "workflow_definitions_built_in_lane_uniq")
	})

	t.Run("custom definition on the same lane allowed", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO workflow_definitions (lane, name) VALUES ('review', 'custom-review')`); err != nil {
			t.Fatalf("custom (non-built-in) review definition rejected: %v", err)
		}
	})

	t.Run("second global binding per lane rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', NULL, $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23505", "workflow_bindings_global_uniq")
	})

	t.Run("repo override binding allowed alongside the global row", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', 'acme/api', $1, 1)`, builtInReviewDefID); err != nil {
			t.Fatalf("repo-override binding rejected: %v", err)
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('review', 'acme/api', $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23505", "workflow_bindings_repo_uniq")
	})

	t.Run("binding lane must match definition lane", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
			VALUES ('plan', 'acme/api', $1, 1)`, builtInReviewDefID)
		expectPgErrCode(t, err, "23503", "workflow_bindings_definition_lane_fk")
	})

	t.Run("cross-definition edge unrepresentable", func(t *testing.T) {
		// The (definition, step) composite FK: plan's step 1 does not
		// exist under the review definition, so this edge cannot be
		// stated at all.
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_edges (workflow_definition_id, from_step_id, to_step_id, on_status)
			VALUES ($1, $2, $3, 'ok')`,
			builtInReviewDefID, "00000000-0000-4000-8000-000000000011", builtInPlanStep1ID)
		expectPgErrCode(t, err, "23503", "workflow_edges_to_step_fk")
	})

	t.Run("second edge for the same (step, outcome) rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_edges (workflow_definition_id, from_step_id, to_step_id, on_status)
			VALUES ($1, $2, $3, 'needs_fix')`,
			builtInPlanDefID, builtInPlanStep1ID, builtInPlanStep2ID)
		expectPgErrCode(t, err, "23505", "workflow_edges_from_status_uniq")
	})

	t.Run("bound definition not deletable", func(t *testing.T) {
		// The seeded global binding references it (NO ACTION FK) --
		// §25.4's resolution guarantee cannot be hollowed out by a
		// delete, even before the application-layer is_built_in refusal
		// (see migration 000057's header) exists to also stop this.
		_, err := pool.Exec(ctx, `DELETE FROM workflow_definitions WHERE id = $1`, builtInReviewDefID)
		expectPgErrCode(t, err, "23503", "workflow_bindings_definition_lane_fk")
	})
}

// TestWorkflowSchema_SequentialExecutionIndexes proves the two partial
// unique indexes that make §25.6's strictly-sequential execution model
// structural: at most one running run per session (mirroring
// turns_one_processing_per_session), and at most one live
// (running/awaiting_decision) step attempt per run -- while a SECOND
// attempt row for the same step is legal once the first is terminal,
// which is exactly the re-execution shape §25.5's COUNT(*) iteration
// read depends on.
func TestWorkflowSchema_SequentialExecutionIndexes(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var sessionID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO sessions (spawn_source) VALUES ('web') RETURNING id`).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	var runID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1) RETURNING id`, sessionID, builtInPlanDefID).Scan(&runID); err != nil {
		t.Fatalf("insert workflow run: %v", err)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1)`, sessionID, builtInPlanDefID)
	expectPgErrCode(t, err, "23505", "workflow_runs_one_running_per_session")

	// Parking the first run in needs_review frees the session for a
	// successor run (§25.9's escalation must not hold the session
	// hostage -- see migration 000057's own workflow_runs comment).
	if _, err := pool.Exec(ctx,
		`UPDATE workflow_runs SET status = 'needs_review' WHERE id = $1`, runID); err != nil {
		t.Fatalf("park run in needs_review: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
		VALUES ($1, 'plan', $2, 1)`, sessionID, builtInPlanDefID); err != nil {
		t.Fatalf("successor run alongside a needs_review run rejected: %v", err)
	}

	var attemptID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2) RETURNING id`, runID, builtInPlanStep1ID).Scan(&attemptID); err != nil {
		t.Fatalf("insert step attempt: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2)`, runID, builtInPlanStep2ID)
	expectPgErrCode(t, err, "23505", "workflow_step_runs_one_live_per_run")

	if _, err := pool.Exec(ctx, `
		UPDATE workflow_step_runs SET status = 'completed', outcome_status = 'needs_fix' WHERE id = $1`, attemptID); err != nil {
		t.Fatalf("complete first attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
		VALUES ($1, $2)`, runID, builtInPlanStep1ID); err != nil {
		t.Fatalf("re-execution attempt after a terminal one rejected: %v", err)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM workflow_step_runs WHERE workflow_run_id = $1 AND step_definition_id = $2`,
		runID, builtInPlanStep1ID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempt COUNT(*) = %d, want 2 -- §25.5's loopguard iteration read", attempts)
	}
}
