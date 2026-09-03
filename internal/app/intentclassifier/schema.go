package intentclassifier

import (
	"encoding/json"

	intentdomain "github.com/narvidev/narvi/internal/domain/intent"
)

// templateNameSystem is the prompt_templates.name key this Step's own
// migration seeds (migrations/000033_intent_classifier.up.sql).
const templateNameSystem = "intent_classifier_system"

// reasoningDescription documents the reasoning field's own schema
// description -- unlike confidence (whose description carries the §18.2
// rubric verbatim), this one is this package's own plain field
// documentation, not a shared cross-surface constant.
const reasoningDescription = "A brief (one or two sentence) explanation of why this target/mode was chosen, grounded directly in the input text."

// buildResponseSchema constructs the JSON Schema every classification
// call constrains its structured output to. The confidence field's own
// "description" is intentdomain.ConfidenceRubric VERBATIM (§18.2: "This
// rubric ... lives at the field-description level of the classifier's
// structured-output schema, next to the field it governs, not floated
// separately in a system prompt") -- a single shared constant, referenced
// here, never duplicated per surface (every ingress surface's
// classification call goes through this SAME Service, so there is
// exactly one call site that builds this schema at all).
func buildResponseSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type": "string",
				"enum": []string{intentdomain.TargetReview, intentdomain.TargetRequest},
			},
			"mode": map[string]any{
				"type": "string",
				"enum": []string{intentdomain.ModePlan, intentdomain.ModeBuild},
			},
			"confidence": map[string]any{
				"type":        "string",
				"enum":        []string{intentdomain.ConfidenceHigh, intentdomain.ConfidenceMedium, intentdomain.ConfidenceLow},
				"description": intentdomain.ConfidenceRubric,
			},
			"reasoning": map[string]any{
				"type":        "string",
				"description": reasoningDescription,
			},
		},
		"required":             []string{"target", "mode", "confidence", "reasoning"},
		"additionalProperties": false,
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		// schema is a fixed, hand-written literal with no dynamic
		// content -- a marshal failure here would mean this package
		// itself no longer compiles the intended shape, not a runtime
		// condition any caller could recover from.
		panic("intentclassifier: buildResponseSchema: " + err.Error())
	}
	return raw
}

// responseSchema is built exactly once, at package init -- every
// classification call across every surface shares this SAME schema
// value, never rebuilt per call.
var responseSchema = buildResponseSchema()

// structuredOutput is the shape Complete's raw JSON response unmarshals
// into -- the wire mirror of responseSchema's own required properties.
type structuredOutput struct {
	Target     string `json:"target"`
	Mode       string `json:"mode"`
	Confidence string `json:"confidence"`
	Reasoning  string `json:"reasoning"`
}

// valid reports whether every field of s is one of the enumerated values
// responseSchema itself constrains the model to -- a defense-in-depth
// check against a provider that (despite the schema) returns something
// else, treated as CodeInvalidOutput rather than trusted blindly.
func (s structuredOutput) valid() bool {
	switch s.Target {
	case intentdomain.TargetReview, intentdomain.TargetRequest:
	default:
		return false
	}
	switch s.Mode {
	case intentdomain.ModePlan, intentdomain.ModeBuild:
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
