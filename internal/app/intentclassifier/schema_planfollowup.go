package intentclassifier

import (
	"encoding/json"

	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// templateNamePlanFollowup is the prompt_templates.name key §23's own
// migration seeds (migrations/000074_plan_followup.up.sql) -- mirrors
// templateNameSystem's own identical role for the review-vs-request/
// plan-vs-build category (schema.go), one row per distinct classification
// category (that migration's own doc comment: "there is exactly one row
// per distinct template purpose").
const templateNamePlanFollowup = "intent_classifier_plan_followup"

// planFollowupReasoningDescription mirrors reasoningDescription's own role
// one category over -- this category's own plain field documentation, not
// a shared cross-category constant.
const planFollowupReasoningDescription = "A brief (one or two sentence) explanation of why this reply was read as requesting a plan change (amend) or not (answer), grounded directly in the input text."

// buildPlanFollowupResponseSchema constructs §23.1's own plan_followup
// structured-output schema -- a SEPARATE schema from buildResponseSchema's
// own review-vs-request/plan-vs-build shape (schema.go), since this
// category has a genuinely different Target vocabulary (amend/answer, not
// review/request) and only ONE decision axis (no "mode" field at all --
// amend-vs-answer has nothing analogous to plan-vs-build to ask
// simultaneously). Still the SAME contract/rubric otherwise (§18.1/§18.2):
// confidence's own "description" is intentdomain.ConfidenceRubric VERBATIM,
// the identical shared constant buildResponseSchema references -- §18.2's
// own "a single shared constant ... never duplicated per surface" survives
// unbroken across categories, not just within one.
func buildPlanFollowupResponseSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type": "string",
				"enum": []string{intentdomain.TargetAmend, intentdomain.TargetAnswer},
			},
			"confidence": map[string]any{
				"type":        "string",
				"enum":        []string{intentdomain.ConfidenceHigh, intentdomain.ConfidenceMedium, intentdomain.ConfidenceLow},
				"description": intentdomain.ConfidenceRubric,
			},
			"reasoning": map[string]any{
				"type":        "string",
				"description": planFollowupReasoningDescription,
			},
		},
		"required":             []string{"target", "confidence", "reasoning"},
		"additionalProperties": false,
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		// schema is a fixed, hand-written literal with no dynamic content --
		// mirrors buildResponseSchema's own identical "this can only fail if
		// this package itself no longer compiles the intended shape" panic
		// (schema.go).
		panic("intentclassifier: buildPlanFollowupResponseSchema: " + err.Error())
	}
	return raw
}

// planFollowupResponseSchema is built exactly once, at package init --
// mirrors responseSchema's own identical "built once, shared across every
// call" precedent (schema.go), just for this SEPARATE category.
var planFollowupResponseSchema = buildPlanFollowupResponseSchema()

// planFollowupStructuredOutput is the shape ClassifyPlanFollowup's own
// Complete call unmarshals its raw JSON response into -- the wire mirror of
// planFollowupResponseSchema's own required properties, one field (mode)
// fewer than structuredOutput (schema.go), since this category has no
// second decision axis.
type planFollowupStructuredOutput struct {
	Target     string `json:"target"`
	Confidence string `json:"confidence"`
	Reasoning  string `json:"reasoning"`
}

// valid reports whether every field of s is one of the enumerated values
// planFollowupResponseSchema itself constrains the model to -- mirrors
// structuredOutput.valid's own identical defense-in-depth role (schema.go):
// a provider that (despite the schema) returns something else is treated as
// CodeInvalidOutput rather than trusted blindly.
func (s planFollowupStructuredOutput) valid() bool {
	switch s.Target {
	case intentdomain.TargetAmend, intentdomain.TargetAnswer:
	default:
		return false
	}
	switch s.Confidence {
	case intentdomain.ConfidenceHigh, intentdomain.ConfidenceMedium, intentdomain.ConfidenceLow:
	default:
		return false
	}
	return true
}
