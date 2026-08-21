//go:build integration

// Integration tests for §25.10/§25.11's own ("workflow definition & run
// API", §25.10/§25.11) definition-authoring routes -- workflowdefinitions.go
// -- against a real Postgres instance, sharing this package's own testRig
// (httpapi_integration_test.go) and createUserWithRole/createSessionForUser
// (planapprove_integration_test.go).
//
// The 3 built-in workflow definitions (review/request/plan, migration
// 000057) and their 3 global bindings are present in every test's own
// pristine, reset database (sharedpool_integration_test.go's own
// seed-restore) -- builtInReviewDefID/builtInRequestDefID/builtInPlanDefID
// below are the SAME fixed seed ids workflow_seed_integration_test.go's
// own constants name (package postgres_test, unexported there, so
// re-declared here rather than exported cross-package purely for tests).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

const (
	builtInReviewDefID  = "00000000-0000-4000-8000-000000000001"
	builtInRequestDefID = "00000000-0000-4000-8000-000000000002"
	builtInPlanDefID    = "00000000-0000-4000-8000-000000000003"
)

// newTestStep builds a minimal, valid, single-step-shaped
// restdtos.WorkflowStepDefinition -- id must be a real uuid (a canvas
// editor's own client-generated node id, or an existing step's own id
// echoed back); order defaults every OTHER field to the SAME zero-config
// passthrough shape the 3 built-ins use (§25.8).
func newTestStep(id string, order int) restdtos.WorkflowStepDefinition {
	return restdtos.WorkflowStepDefinition{
		Id:                     id,
		Order:                  order,
		Kind:                   restdtos.WorkflowStepDefinitionKindAgent,
		ModelId:                nil,
		Effort:                 nil,
		PromptTemplate:         "{{prompt}}",
		ExecutionScope:         restdtos.WorkflowStepDefinitionExecutionScopeSameSession,
		ConversationContinuity: restdtos.WorkflowStepDefinitionConversationContinuityContinue,
		HitlBefore:             false,
		HitlAfter:              false,
		Edges:                  nil,
	}
}

// mustJSON marshals v, failing the test on error -- every request body
// this file's own tests build.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// bindDefinitionGlobally binds definitionID as lane's own global
// binding, directly via SQL (bypassing the REST route entirely) --
// this file's own "a custom definition that is bound" fixture, mirroring
// seedAwaitingDecisionRun's own "surgical direct DB seed" precedent
// (decideworkflowstep_integration_test.go): ON CONFLICT here because
// EVERY lane already has a global binding (the migration 000057 seed),
// so this REPOINTS it rather than inserting a second row.
func bindDefinitionGlobally(ctx context.Context, t *testing.T, r testRig, lane, definitionID string) {
	t.Helper()
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO workflow_bindings (lane, repo_full_name, workflow_definition_id, definition_version)
		 VALUES ($1, NULL, $2, 1)
		 ON CONFLICT (lane) WHERE repo_full_name IS NULL
		 DO UPDATE SET workflow_definition_id = EXCLUDED.workflow_definition_id, updated_at = now()`,
		lane, definitionID); err != nil {
		t.Fatalf("bind definition globally: %v", err)
	}
}

// createCustomDefinition inserts an unbound, custom (is_built_in=false)
// single-step workflow_definitions row directly via SQL -- this file's
// own "a custom definition" fixture (unbound unless the caller separately
// calls bindDefinitionGlobally).
func createCustomDefinition(ctx context.Context, t *testing.T, r testRig, lane, name string) (defID, stepID string) {
	t.Helper()
	if err := r.pool.QueryRow(ctx,
		`INSERT INTO workflow_definitions (lane, name, is_built_in, version) VALUES ($1, $2, false, 1) RETURNING id`,
		lane, name).Scan(&defID); err != nil {
		t.Fatalf("insert custom workflow_definitions row: %v", err)
	}
	if err := r.pool.QueryRow(ctx,
		`INSERT INTO workflow_step_definitions (workflow_definition_id, step_order, kind, prompt_template) VALUES ($1, 1, 'agent', '{{prompt}}') RETURNING id`,
		defID).Scan(&stepID); err != nil {
		t.Fatalf("insert custom workflow_step_definitions row: %v", err)
	}
	return defID, stepID
}

// --- GET (list/one) ---

func TestListWorkflowDefinitions_IncludesTheThreeBuiltIns(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var resp restdtos.ListWorkflowDefinitionsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/workflow-definitions", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	seen := map[string]bool{}
	for _, d := range resp.Definitions {
		seen[d.Id] = true
		if d.Id == builtInReviewDefID || d.Id == builtInRequestDefID || d.Id == builtInPlanDefID {
			if !d.IsBuiltIn {
				t.Errorf("definition %s: IsBuiltIn = false, want true", d.Id)
			}
			if len(d.Steps) == 0 {
				t.Errorf("definition %s: Steps is empty, want at least 1", d.Id)
			}
		}
	}
	for _, want := range []string{builtInReviewDefID, builtInRequestDefID, builtInPlanDefID} {
		if !seen[want] {
			t.Errorf("built-in definition %s missing from list", want)
		}
	}
}

func TestListWorkflowDefinitions_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/workflow-definitions", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (maintainer+ only, §25.11)", status, http.StatusForbidden)
	}
}

func TestGetWorkflowDefinition_NotFound_Returns404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodGet, "/api/workflow-definitions/00000000-0000-0000-0000-000000000000", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestGetWorkflowDefinition_BuiltInReview_ShapeMatchesSeed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.WorkflowDefinition
	status := rig.doJSON(t, http.MethodGet, "/api/workflow-definitions/"+builtInReviewDefID, nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !got.IsBuiltIn || got.Lane != restdtos.WorkflowDefinitionLaneReview || len(got.Steps) != 1 {
		t.Errorf("got = (isBuiltIn %v, lane %q, %d steps), want (true, review, 1 step)", got.IsBuiltIn, got.Lane, len(got.Steps))
	}
	if len(got.Steps) == 1 && len(got.Steps[0].Edges) != 0 {
		t.Errorf("built-in review step has %d edges, want 0 (pure passthrough, §25.8)", len(got.Steps[0].Edges))
	}
}

// --- POST create: whole-document mode ---

func TestCreateWorkflowDefinition_WholeDocument_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	auditID := uuid.NewString()
	fixID := uuid.NewString()
	req := restdtos.CreateWorkflowDefinitionRequest{
		SourceDefinitionId: nil,
		Name:               "audit-then-fix",
		Lane:               ptr(restdtos.CreateWorkflowDefinitionRequestLaneRequest),
		Steps: []restdtos.WorkflowStepDefinition{
			func() restdtos.WorkflowStepDefinition {
				s := newTestStep(auditID, 1)
				s.Edges = []restdtos.WorkflowEdge{{FromStepId: auditID, OnStatus: restdtos.WorkflowEdgeOnStatusNeedsFix, ToStepId: fixID}}
				return s
			}(),
			newTestStep(fixID, 2),
		},
	}

	var created restdtos.WorkflowDefinition
	status := rig.doJSON(t, http.MethodPost, "/api/workflow-definitions", mustJSON(t, req), &created, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if created.IsBuiltIn || created.Version != 1 || created.Lane != restdtos.WorkflowDefinitionLaneRequest || created.Name != "audit-then-fix" {
		t.Fatalf("created = %+v, want isBuiltIn=false version=1 lane=request name=audit-then-fix", created)
	}
	if len(created.Steps) != 2 {
		t.Fatalf("created has %d steps, want 2", len(created.Steps))
	}

	var fetched restdtos.WorkflowDefinition
	status = rig.doJSON(t, http.MethodGet, "/api/workflow-definitions/"+created.Id, nil, &fetched, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if fetched.Name != created.Name || fetched.Version != created.Version || len(fetched.Steps) != 2 {
		t.Errorf("GET fetched %+v, want it to match the just-created document", fetched)
	}
	foundEdge := false
	for _, s := range fetched.Steps {
		if s.Id == auditID {
			if len(s.Edges) != 1 || s.Edges[0].ToStepId != fixID || s.Edges[0].OnStatus != restdtos.WorkflowEdgeOnStatusNeedsFix {
				t.Errorf("audit step edges = %+v, want one needs_fix -> fix edge", s.Edges)
			} else {
				foundEdge = true
			}
		}
	}
	if !foundEdge {
		t.Errorf("audit step %s not found in fetched steps %+v", auditID, fetched.Steps)
	}
}

func TestCreateWorkflowDefinition_WholeDocument_MissingLane_Returns400(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	req := restdtos.CreateWorkflowDefinitionRequest{
		Name:  "no-lane",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(uuid.NewString(), 1)},
	}
	status := rig.doJSON(t, http.MethodPost, "/api/workflow-definitions", mustJSON(t, req), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (lane required in whole-document mode)", status, http.StatusBadRequest)
	}
}

// --- POST create: duplicate mode -- the explicitly required "copying a
// built-in yields an editable, unbound, non-built-in definition at
// version 1" test. ---

func TestCreateWorkflowDefinition_DuplicateBuiltIn_YieldsEditableUnboundCopy(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	req := restdtos.CreateWorkflowDefinitionRequest{
		SourceDefinitionId: ptr(builtInReviewDefID),
		Name:               "review-custom",
	}
	var copyDoc restdtos.WorkflowDefinition
	status := rig.doJSON(t, http.MethodPost, "/api/workflow-definitions", mustJSON(t, req), &copyDoc, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if copyDoc.IsBuiltIn {
		t.Errorf("copy IsBuiltIn = true, want false")
	}
	if copyDoc.Version != 1 {
		t.Errorf("copy Version = %d, want 1", copyDoc.Version)
	}
	if copyDoc.Id == builtInReviewDefID {
		t.Errorf("copy Id = source id, want a genuinely new id")
	}
	if copyDoc.Lane != restdtos.WorkflowDefinitionLaneReview {
		t.Errorf("copy Lane = %q, want review (inherited from source)", copyDoc.Lane)
	}
	if len(copyDoc.Steps) != 1 {
		t.Fatalf("copy has %d steps, want 1 (deep-copied from the source)", len(copyDoc.Steps))
	}
	if copyDoc.Steps[0].Id == "" {
		t.Errorf("copy step has empty id")
	}

	// "Editable": a maintainer can now PUT the copy (proving it is
	// genuinely unbound -- a bound definition would be refused, see the
	// refusal tests below).
	newStepID := copyDoc.Steps[0].Id
	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name:  "review-custom-edited",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(newStepID, 1)},
	}
	var updated restdtos.WorkflowDefinition
	status = rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+copyDoc.Id, mustJSON(t, putReq), &updated, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (copy must be editable -- it is unbound)", status, http.StatusOK)
	}
	if updated.Name != "review-custom-edited" || updated.Version != 2 {
		t.Errorf("updated = (name %q, version %d), want (review-custom-edited, 2)", updated.Name, updated.Version)
	}
}

func TestCreateWorkflowDefinition_DuplicateUnknownSource_Returns404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	req := restdtos.CreateWorkflowDefinitionRequest{
		SourceDefinitionId: ptr("00000000-0000-0000-0000-000000000000"),
		Name:               "copy-of-nothing",
	}
	status := rig.doJSON(t, http.MethodPost, "/api/workflow-definitions", mustJSON(t, req), nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- PUT round-trip: "PUT a definition, GET it back, compare field by
// field including steps and edges." ---

func TestPutWorkflowDefinition_DocumentRoundTrip_FieldByField(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	defID, _ := createCustomDefinition(ctx, t, rig, "request", "put-roundtrip")

	step1 := uuid.NewString()
	step2 := uuid.NewString()
	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name: "put-roundtrip-v2",
		Steps: []restdtos.WorkflowStepDefinition{
			func() restdtos.WorkflowStepDefinition {
				s := newTestStep(step1, 1)
				s.HitlAfter = true
				s.Edges = []restdtos.WorkflowEdge{{FromStepId: step1, OnStatus: restdtos.WorkflowEdgeOnStatusOk, ToStepId: step2}}
				// A round trip that leaves every optional field at its zero
				// value proves nothing about the optional fields. modelId and
				// effort are nullable passthroughs, and canvasPosition is the
				// ONLY step field on this surface with hand-written encode and
				// decode (canvasPositionToJSON/FromJSON) -- so it is the field
				// most able to be silently dropped, and the earlier version of
				// this test never constructed it.
				modelID := "anthropic/claude-opus-5"
				s.ModelId = &modelID
				effort := "high"
				s.Effort = &effort
				s.CanvasPosition = &restdtos.WorkflowStepDefinitionCanvasPosition{X: 12.5, Y: -40}
				return s
			}(),
			func() restdtos.WorkflowStepDefinition {
				s := newTestStep(step2, 2)
				s.ConversationContinuity = restdtos.WorkflowStepDefinitionConversationContinuityFresh
				s.HitlBefore = true
				return s
			}(),
		},
	}

	var putResp restdtos.WorkflowDefinition
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+defID, mustJSON(t, putReq), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}

	var getResp restdtos.WorkflowDefinition
	status = rig.doJSON(t, http.MethodGet, "/api/workflow-definitions/"+defID, nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}

	for name, got := range map[string]restdtos.WorkflowDefinition{"PUT response": putResp, "GET response": getResp} {
		if got.Id != defID {
			t.Errorf("%s: Id = %q, want %q", name, got.Id, defID)
		}
		if got.Name != "put-roundtrip-v2" {
			t.Errorf("%s: Name = %q, want put-roundtrip-v2", name, got.Name)
		}
		if got.Version != 2 {
			t.Errorf("%s: Version = %d, want 2 (bumped from 1)", name, got.Version)
		}
		if got.IsBuiltIn {
			t.Errorf("%s: IsBuiltIn = true, want false", name)
		}
		if len(got.Steps) != 2 {
			t.Fatalf("%s: has %d steps, want 2", name, len(got.Steps))
		}
		byID := map[string]restdtos.WorkflowStepDefinition{}
		for _, s := range got.Steps {
			byID[s.Id] = s
		}
		s1, ok := byID[step1]
		if !ok {
			t.Fatalf("%s: step %s missing", name, step1)
		}
		if s1.Order != 1 || !s1.HitlAfter {
			t.Errorf("%s: step1 = (order %d, hitlAfter %v), want (1, true)", name, s1.Order, s1.HitlAfter)
		}
		if len(s1.Edges) != 1 || s1.Edges[0].ToStepId != step2 || s1.Edges[0].OnStatus != restdtos.WorkflowEdgeOnStatusOk {
			t.Errorf("%s: step1 edges = %+v, want one ok -> step2 edge", name, s1.Edges)
		}
		s2, ok := byID[step2]
		if !ok {
			t.Fatalf("%s: step %s missing", name, step2)
		}
		if s2.Order != 2 || s2.ConversationContinuity != restdtos.WorkflowStepDefinitionConversationContinuityFresh {
			t.Errorf("%s: step2 = (order %d, continuity %q), want (2, fresh)", name, s2.Order, s2.ConversationContinuity)
		}

		// The fields the test's own name promises and the earlier version
		// never sent or compared.
		if s1.PromptTemplate != "{{prompt}}" {
			t.Errorf("%s: step1 PromptTemplate = %q, want {{prompt}}", name, s1.PromptTemplate)
		}
		if s1.Kind != restdtos.WorkflowStepDefinitionKindAgent {
			t.Errorf("%s: step1 Kind = %q, want agent", name, s1.Kind)
		}
		if s1.ExecutionScope != restdtos.WorkflowStepDefinitionExecutionScopeSameSession {
			t.Errorf("%s: step1 ExecutionScope = %q, want same_session", name, s1.ExecutionScope)
		}
		if s1.ModelId == nil || *s1.ModelId != "anthropic/claude-opus-5" {
			t.Errorf("%s: step1 ModelId = %v, want anthropic/claude-opus-5", name, s1.ModelId)
		}
		if s1.Effort == nil || *s1.Effort != "high" {
			t.Errorf("%s: step1 Effort = %v, want high", name, s1.Effort)
		}
		if s1.CanvasPosition == nil {
			t.Errorf("%s: step1 CanvasPosition is nil, want {12.5, -40} -- the hand-encoded field", name)
		} else if s1.CanvasPosition.X != 12.5 || s1.CanvasPosition.Y != -40 {
			t.Errorf("%s: step1 CanvasPosition = %+v, want {12.5, -40}", name, *s1.CanvasPosition)
		}
		if s1.HitlBefore {
			t.Errorf("%s: step1 HitlBefore = true, want false", name)
		}
		if !s2.HitlBefore {
			t.Errorf("%s: step2 HitlBefore = false, want true", name)
		}
		if s2.ModelId != nil || s2.Effort != nil || s2.CanvasPosition != nil {
			t.Errorf("%s: step2 optional fields should round-trip as null, got model=%v effort=%v canvas=%v", name, s2.ModelId, s2.Effort, s2.CanvasPosition)
		}
	}
}

// --- The two structural refusals, including for an admin. ---

func TestPutWorkflowDefinition_BuiltIn_RefusedEvenForAdmin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name:  "hijacked-review",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(uuid.NewString(), 1)},
	}
	var body map[string]string
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+builtInReviewDefID, mustJSON(t, putReq), &body, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d (built-in refusal is unconditional, even for an admin)", status, http.StatusConflict)
	}
	if msg := body["error"]; msg == "" || !containsFold(msg, "built-in") {
		t.Errorf("error message = %q, want it to name the built-in refusal specifically", msg)
	}
}

func TestDeleteWorkflowDefinition_BuiltIn_RefusedEvenForAdmin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var body map[string]string
	status := rig.doJSON(t, http.MethodDelete, "/api/workflow-definitions/"+builtInRequestDefID, nil, &body, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d (built-in refusal is unconditional, even for an admin)", status, http.StatusConflict)
	}
	if msg := body["error"]; msg == "" || !containsFold(msg, "built-in") {
		t.Errorf("error message = %q, want it to name the built-in refusal specifically", msg)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_definitions WHERE id = $1`, builtInRequestDefID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("built-in request definition row count = %d after refused delete, want 1 (never actually deleted)", count)
	}
}

// TestPutWorkflowDefinition_BoundCustom_RefusedEvenForAdmin and
// TestDeleteWorkflowDefinition_BoundCustom_RefusedEvenForAdmin cover the
// SECOND structural refusal on a definition where is_built_in=false --
// isolating it from the built-in check. That isolation matters because the
// three seeded lane defaults are built-in AND globally bound (§25.8, and
// migration 000057's own seed), so on a fresh database both refusals fire on
// the same rows -- a test using only a built-in would leave the bound refusal
// entirely unexercised. Neither §25.10 nor §25.11 states this; it is a
// property of the seed, said here rather than attributed to a section that
// does not carry it.
func TestPutWorkflowDefinition_BoundCustom_RefusedEvenForAdmin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	defID, _ := createCustomDefinition(ctx, t, rig, "request", "bound-custom-put")
	bindDefinitionGlobally(ctx, t, rig, "request", defID)

	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name:  "bound-custom-put-edited",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(uuid.NewString(), 1)},
	}
	var body map[string]string
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+defID, mustJSON(t, putReq), &body, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d (bound refusal is unconditional, even for an admin, even on a non-built-in definition)", status, http.StatusConflict)
	}
	if msg := body["error"]; msg == "" || !containsFold(msg, "bound") {
		t.Errorf("error message = %q, want it to name the bound refusal specifically (not the built-in one)", msg)
	}
}

func TestDeleteWorkflowDefinition_BoundCustom_RefusedEvenForAdmin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	defID, _ := createCustomDefinition(ctx, t, rig, "request", "bound-custom-delete")
	bindDefinitionGlobally(ctx, t, rig, "request", defID)

	var body map[string]string
	status := rig.doJSON(t, http.MethodDelete, "/api/workflow-definitions/"+defID, nil, &body, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
	if msg := body["error"]; msg == "" || !containsFold(msg, "bound") {
		t.Errorf("error message = %q, want it to name the bound refusal specifically", msg)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_definitions WHERE id = $1`, defID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("bound custom definition row count = %d after refused delete, want 1", count)
	}
}

// --- RBAC matrix: maintainer can edit an unbound draft; maintainer
// CANNOT activate a binding; admin can (the binding half lives in
// workflowbindings_integration_test.go). ---

func TestPutWorkflowDefinition_MaintainerCanEditUnboundDraft(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	defID, stepID := createCustomDefinition(ctx, t, rig, "request", "maintainer-editable")

	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name:  "maintainer-editable-v2",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(stepID, 1)},
	}
	var updated restdtos.WorkflowDefinition
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+defID, mustJSON(t, putReq), &updated, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (maintainer holds authz.ActionManageWorkflowDefinitions, §25.11)", status, http.StatusOK)
	}
	if updated.Name != "maintainer-editable-v2" {
		t.Errorf("Name = %q, want maintainer-editable-v2", updated.Name)
	}
}

func TestPutWorkflowDefinition_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.
	defID, stepID := createCustomDefinition(ctx, t, rig, "request", "member-denied")

	putReq := restdtos.UpdateWorkflowDefinitionRequest{
		Name:  "member-denied-v2",
		Steps: []restdtos.WorkflowStepDefinition{newTestStep(stepID, 1)},
	}
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+defID, mustJSON(t, putReq), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// --- "A PUT that would produce an unexecutable graph is refused, with
// the specific rule named -- one case per validation rule." Covers the
// rules a client CAN violate through the wire (kind/executionScope/
// conversationContinuity/onStatus are all closed schema-level enums
// already rejected at JSON-decode time, before ever reaching
// ValidateDefinition -- not exercised again here). ---

func TestPutWorkflowDefinition_UnexecutableGraph_RefusedWithSpecificRule(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	tests := []struct {
		name       string
		buildSteps func() []restdtos.WorkflowStepDefinition
		wantInMsg  string
	}{
		{
			name: "duplicate step order",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				return []restdtos.WorkflowStepDefinition{newTestStep(uuid.NewString(), 1), newTestStep(uuid.NewString(), 1)}
			},
			wantInMsg: "duplicate",
		},
		{
			name: "invalid step order (< 1)",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				return []restdtos.WorkflowStepDefinition{newTestStep(uuid.NewString(), 0)}
			},
			wantInMsg: "order",
		},
		{
			name: "duplicate step id",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				id := uuid.NewString()
				return []restdtos.WorkflowStepDefinition{newTestStep(id, 1), newTestStep(id, 2)}
			},
			wantInMsg: "duplicate",
		},
		{
			name: "empty prompt template",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				s := newTestStep(uuid.NewString(), 1)
				s.PromptTemplate = ""
				return []restdtos.WorkflowStepDefinition{s}
			},
			wantInMsg: "prompt",
		},
		{
			name: "edge targets a step not in this definition",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				id := uuid.NewString()
				s := newTestStep(id, 1)
				s.Edges = []restdtos.WorkflowEdge{{FromStepId: id, OnStatus: restdtos.WorkflowEdgeOnStatusOk, ToStepId: uuid.NewString()}}
				return []restdtos.WorkflowStepDefinition{s}
			},
			wantInMsg: "target",
		},
		{
			name: "duplicate edge for (step, on-status)",
			buildSteps: func() []restdtos.WorkflowStepDefinition {
				id1 := uuid.NewString()
				id2 := uuid.NewString()
				s := newTestStep(id1, 1)
				s.Edges = []restdtos.WorkflowEdge{
					{FromStepId: id1, OnStatus: restdtos.WorkflowEdgeOnStatusOk, ToStepId: id2},
					{FromStepId: id1, OnStatus: restdtos.WorkflowEdgeOnStatusOk, ToStepId: id2},
				}
				return []restdtos.WorkflowStepDefinition{s, newTestStep(id2, 2)}
			},
			wantInMsg: "duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defID, _ := createCustomDefinition(ctx, t, rig, "request", fmt.Sprintf("invalid-%s-%s", tt.name, uuid.NewString()))
			putReq := restdtos.UpdateWorkflowDefinitionRequest{Name: "invalid-graph", Steps: tt.buildSteps()}

			var body map[string]string
			status := rig.doJSON(t, http.MethodPut, "/api/workflow-definitions/"+defID, mustJSON(t, putReq), &body, token)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
			}
			if msg := body["error"]; !containsFold(msg, tt.wantInMsg) {
				t.Errorf("error message = %q, want it to mention %q", msg, tt.wantInMsg)
			}

			// The refused PUT must never have mutated the definition's own
			// row -- version stays 1, name stays the original.
			var version int
			var name string
			if err := rig.pool.QueryRow(ctx, `SELECT version, name FROM workflow_definitions WHERE id = $1`, defID).Scan(&version, &name); err != nil {
				t.Fatalf("query: %v", err)
			}
			if version != 1 {
				t.Errorf("version = %d after refused PUT, want 1 (never bumped)", version)
			}
			if name == "invalid-graph" {
				t.Errorf("name = %q after refused PUT, want it unchanged from creation", name)
			}
		})
	}
}

// containsFold reports whether s contains substr, case-insensitively --
// a small local helper so error-message assertions above don't depend on
// exact casing.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// ptr returns a pointer to v -- a small generic helper for building
// request DTOs whose optional fields are typed pointers.
func ptr[T any](v T) *T { return &v }

// TestGetWorkflowDefinitions_EdgesAreAlwaysAnArrayOnTheWire asserts on the RAW
// response bytes, not on a decoded struct, and that distinction is the whole
// point.
//
// Go's encoding/json decodes both `null` and `[]` into a nil slice, so every
// existing test here — which decodes into restdtos.WorkflowDefinition and
// checks len(Edges) — passes identically whether the server emitted an array
// or a null. The schema declares edges as a required, non-nullable array and
// the generated TypeScript types it WorkflowEdge[], so a canvas calling
// step.edges.map(...) would throw on null. Every seeded built-in is a
// single-step passthrough with zero edges, so this is the common case.
//
// Member.identities already cost this codebase the identical finding; this is
// the same assertion shape, applied before a consumer exists rather than after
// one crashed.
func TestGetWorkflowDefinitions_EdgesAreAlwaysAnArrayOnTheWire(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	req, err := http.NewRequest(http.MethodGet, rig.server.URL+"/api/workflow-definitions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if bytes.Contains(raw, []byte(`"edges":null`)) {
		t.Errorf("response contains \"edges\":null; the schema declares edges as a required non-nullable array.\nbody: %s", raw)
	}
	// And prove the assertion above is not vacuous: the seeded built-ins must
	// actually be present, each with an edges key.
	if !bytes.Contains(raw, []byte(`"edges":[]`)) {
		t.Errorf("no empty edges array found; the three seeded built-ins are single-step passthroughs and should each carry one.\nbody: %s", raw)
	}
}
