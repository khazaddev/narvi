package workflow

import (
	"errors"
	"fmt"
)

// ID is a workflow definition's or step definition's identifier, kept a
// plain string rather than pgtype.UUID/uuid.UUID so this package stays
// adapter-independent -- mirroring internal/domain/plan.ID's own
// precedent (§11); callers convert at the boundary.
type ID string

// Definition is one workflow_definitions row plus its ordered
// steps -- §25.4's exact field list. IsBuiltIn marks one of the three
// seeded system templates (migrations/000057_workflows.up.sql), whose
// PUT/DELETE-refused-unconditionally invariant is structural, not RBAC
// (see doc.go). Version is a 1-based edit counter (a binding pins the
// version it was activated against -- workflow_bindings.definition_
// version); it is provenance metadata, not a versioned-content archive
// (no historical-content table exists, deliberately -- §25.4 models
// Version as a field OF the definition, never a side table).
type Definition struct {
	ID        ID
	Lane      Lane
	Name      string
	IsBuiltIn bool
	Version   int
	Steps     []StepDefinition
}

// StepKind is what a step IS -- a closed enum matching the
// workflow_step_kind Postgres enum exactly.
type StepKind string

// StepKindAgent -- the only StepKind as of Step 54: an ordinary agent
// turn (prompt + model), which is what EVERY step in every §25.8 shape
// (the three built-ins and the Gemini->Opus->Sonnet->Codex override
// example) is, and what §25.6's execution model dispatches ("every step
// is an ordinary sequential turn"). §25.4 lists Kind as a field without
// enumerating values anywhere in §25 -- a single-value closed enum is
// the honest reading: the field exists so a later phase (e.g. the
// canvas editor's own vocabulary growth, §25.12) can add a non-agent
// kind without a shape change, exactly like reserved RBAC actions, and
// nothing is invented beyond what the plan's own shapes exercise.
const StepKindAgent StepKind = "agent"

// AllStepKinds is every recognized StepKind, in declaration order.
var AllStepKinds = []StepKind{StepKindAgent}

// IsValidStepKind reports whether k is a recognized StepKind.
func IsValidStepKind(k StepKind) bool {
	return k == StepKindAgent
}

// ExecutionScope is where a step's turn runs (§25.6) -- a closed enum
// matching the workflow_execution_scope Postgres enum exactly.
type ExecutionScope string

const (
	// ExecutionScopeSameSession dispatches the step as an ordinary
	// sequential turn on the SAME sessions row -- the default, and what
	// every built-in step uses (§25.6: "no new wire command, no
	// AgentRuntime change").
	ExecutionScopeSameSession ExecutionScope = "same_session"
	// ExecutionScopeChildSession dispatches the step in a child session
	// -- reserved for steps needing real isolation (§25.6: "the
	// audit-fix loop's fix step alone, never the audit step itself"),
	// following Step 48's provenance-tag restriction discipline, never a
	// numeric-depth mechanism.
	ExecutionScopeChildSession ExecutionScope = "child_session"
)

// AllExecutionScopes is every recognized ExecutionScope, in declaration
// order.
var AllExecutionScopes = []ExecutionScope{ExecutionScopeSameSession, ExecutionScopeChildSession}

// IsValidExecutionScope reports whether s is a recognized ExecutionScope.
func IsValidExecutionScope(s ExecutionScope) bool {
	switch s {
	case ExecutionScopeSameSession, ExecutionScopeChildSession:
		return true
	}
	return false
}

// ConversationContinuity is whether a step inherits the chat history of
// the steps before it (§25.6) -- a closed enum matching the
// workflow_conversation_continuity Postgres enum exactly. "Fresh context
// is not a new session": fresh means a new OpenCode conversation inside
// the SAME sandbox/session (AgentRuntime.StartTurn's own nil
// ConversationId branch), never a child session.
type ConversationContinuity string

const (
	// ConversationContinuityContinue continues the session's existing
	// OpenCode conversation -- the default, today's exact behavior.
	ConversationContinuityContinue ConversationContinuity = "continue"
	// ConversationContinuityFresh starts a new OpenCode conversation on
	// the same session, for a step that must not inherit earlier steps'
	// full chat history.
	ConversationContinuityFresh ConversationContinuity = "fresh"
)

// AllConversationContinuities is every recognized ConversationContinuity,
// in declaration order.
var AllConversationContinuities = []ConversationContinuity{ConversationContinuityContinue, ConversationContinuityFresh}

// IsValidConversationContinuity reports whether c is a recognized
// ConversationContinuity.
func IsValidConversationContinuity(c ConversationContinuity) bool {
	switch c {
	case ConversationContinuityContinue, ConversationContinuityFresh:
		return true
	}
	return false
}

// StepDefinition is one workflow_step_definitions row plus its outgoing
// edges -- §25.4's exact field list. ModelID is nil to inherit exactly
// what the session would use today (turns.model_id /
// sessions.build_model_id -- §25.8's zero-config proof), otherwise the
// same "provider/model" passthrough convention §25.1/§25.7 verified
// (never a Narvi-side allowlist). PromptTemplate uses the established
// "{{variable_name}}" placeholder syntax (§18.6,
// internal/domain/intent.AssembleTemplate) -- "{{prompt}}" is the
// caller's own text, making the built-in review/request steps a pure
// passthrough.
type StepDefinition struct {
	ID      ID
	Order   int
	Kind    StepKind
	ModelID *string
	// Effort mirrors ModelID's own shape and semantics exactly (Step 59,
	// §29.8's "workflow engine echo"): nil inherits exactly what the
	// session would use today (turns.effort/sessions.build_effort), a
	// non-nil value overrides it for this step -- the same "provider/
	// model" passthrough discipline §25.1/§25.7 established for ModelID,
	// with no Narvi-side allowlist here either (valid values are owned
	// per-model by OpenCode's own catalog `variants` maps, §29.8).
	Effort                 *string
	PromptTemplate         string
	ExecutionScope         ExecutionScope
	ConversationContinuity ConversationContinuity
	HITLBefore             bool
	HITLAfter              bool
	Edges                  []Edge
}

// Edge is one explicit (from step, outcome) -> to step routing rule
// (§25.4) -- one workflow_edges row. FromStepID is carried explicitly
// even though an Edge lives on its from-step's own Edges slice (the
// spec's own field list includes it, and the flat workflow_edges table
// needs it); ValidateDefinition enforces the two never disagree.
// OnStatus is the ONLY thing an edge may condition on -- the closed
// StepOutcomeStatus vocabulary, never Shippable, never an expression
// (§25.4, §25.12).
type Edge struct {
	FromStepID ID
	OnStatus   StepOutcomeStatus
	ToStepID   ID
}

// Validation sentinels -- each wrapped with positional context by
// ValidateDefinition, so callers/tests distinguish failures via
// errors.Is while logs still say exactly which step/edge offended,
// mirroring internal/domain/automation's own sentinel-plus-context
// validation shape.
var (
	ErrInvalidLane                   = errors.New("workflow: invalid lane")
	ErrEmptyName                     = errors.New("workflow: empty definition name")
	ErrInvalidVersion                = errors.New("workflow: version must be >= 1")
	ErrNoSteps                       = errors.New("workflow: definition has no steps")
	ErrEmptyStepID                   = errors.New("workflow: empty step id")
	ErrDuplicateStepID               = errors.New("workflow: duplicate step id")
	ErrInvalidStepOrder              = errors.New("workflow: step order must be >= 1")
	ErrDuplicateStepOrder            = errors.New("workflow: duplicate step order")
	ErrInvalidStepKind               = errors.New("workflow: invalid step kind")
	ErrEmptyModelID                  = errors.New("workflow: model id set but empty")
	ErrEmptyEffort                   = errors.New("workflow: effort set but empty")
	ErrEmptyPromptTemplate           = errors.New("workflow: empty prompt template")
	ErrInvalidExecutionScope         = errors.New("workflow: invalid execution scope")
	ErrInvalidConversationContinuity = errors.New("workflow: invalid conversation continuity")
	ErrEdgeFromMismatch              = errors.New("workflow: edge FromStepID differs from its owning step")
	ErrInvalidEdgeStatus             = errors.New("workflow: invalid edge on-status")
	ErrEdgeUnknownTarget             = errors.New("workflow: edge targets a step not in this definition")
	ErrDuplicateEdge                 = errors.New("workflow: duplicate edge for (step, on-status)")
)

// ValidateDefinition is the single validation authority for whether def
// is a well-formed workflow the engine's closed model can execute --
// exactly the check §25.12 requires the canvas editor to apply at save
// time ("rejecting an undrawable-by-the-engine graph at save time, not
// silently accepting it"), owned here so Steps 55-56 (load-time
// defense) and Step 88 (save-time gate) share ONE rule set, never two
// drifting copies. First violation wins; nil means def is executable.
//
// Orders must be unique and >= 1 but NOT contiguous -- NextStep's
// default advance is "the smallest Order strictly greater than the
// current step's", which is well-defined with gaps, so contiguity is a
// presentation nicety this validation deliberately does not impose.
func ValidateDefinition(def Definition) error {
	if !IsValidLane(def.Lane) {
		return fmt.Errorf("%w: %q", ErrInvalidLane, def.Lane)
	}
	if def.Name == "" {
		return ErrEmptyName
	}
	if def.Version < 1 {
		return fmt.Errorf("%w: got %d", ErrInvalidVersion, def.Version)
	}
	if len(def.Steps) == 0 {
		return ErrNoSteps
	}

	stepIDs := make(map[ID]bool, len(def.Steps))
	orders := make(map[int]bool, len(def.Steps))
	for _, step := range def.Steps {
		if step.ID == "" {
			return ErrEmptyStepID
		}
		if stepIDs[step.ID] {
			return fmt.Errorf("%w: %q", ErrDuplicateStepID, step.ID)
		}
		stepIDs[step.ID] = true

		if step.Order < 1 {
			return fmt.Errorf("%w: step %q has order %d", ErrInvalidStepOrder, step.ID, step.Order)
		}
		if orders[step.Order] {
			return fmt.Errorf("%w: order %d", ErrDuplicateStepOrder, step.Order)
		}
		orders[step.Order] = true

		if !IsValidStepKind(step.Kind) {
			return fmt.Errorf("%w: step %q has kind %q", ErrInvalidStepKind, step.ID, step.Kind)
		}
		if step.ModelID != nil && *step.ModelID == "" {
			return fmt.Errorf("%w: step %q", ErrEmptyModelID, step.ID)
		}
		if step.Effort != nil && *step.Effort == "" {
			return fmt.Errorf("%w: step %q", ErrEmptyEffort, step.ID)
		}
		if step.PromptTemplate == "" {
			return fmt.Errorf("%w: step %q", ErrEmptyPromptTemplate, step.ID)
		}
		if !IsValidExecutionScope(step.ExecutionScope) {
			return fmt.Errorf("%w: step %q has scope %q", ErrInvalidExecutionScope, step.ID, step.ExecutionScope)
		}
		if !IsValidConversationContinuity(step.ConversationContinuity) {
			return fmt.Errorf("%w: step %q has continuity %q", ErrInvalidConversationContinuity, step.ID, step.ConversationContinuity)
		}
	}

	// Edges are checked in a second pass so a forward edge (to a step
	// declared later in Steps) validates identically to a backward one --
	// edge legality must not depend on slice position.
	for _, step := range def.Steps {
		seen := make(map[StepOutcomeStatus]bool, len(step.Edges))
		for _, edge := range step.Edges {
			if edge.FromStepID != step.ID {
				return fmt.Errorf("%w: step %q carries edge from %q", ErrEdgeFromMismatch, step.ID, edge.FromStepID)
			}
			if !IsValidStepOutcomeStatus(edge.OnStatus) {
				return fmt.Errorf("%w: step %q on %q", ErrInvalidEdgeStatus, step.ID, edge.OnStatus)
			}
			if !stepIDs[edge.ToStepID] {
				return fmt.Errorf("%w: step %q -> %q on %q", ErrEdgeUnknownTarget, step.ID, edge.ToStepID, edge.OnStatus)
			}
			if seen[edge.OnStatus] {
				return fmt.Errorf("%w: step %q on %q", ErrDuplicateEdge, step.ID, edge.OnStatus)
			}
			seen[edge.OnStatus] = true
		}
	}

	return nil
}
