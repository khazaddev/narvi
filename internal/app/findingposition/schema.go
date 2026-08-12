package findingposition

import "encoding/json"

// relocationSystemPrompt is the fixed system prompt every relocation call
// sends -- a small, first-party instruction, never containing the diff or
// snippet themselves (those travel as the user message, buildUserMessage
// below), mirroring intentclassifier's own system/user split.
const relocationSystemPrompt = "You are given one file's own diff hunk (unified diff format, showing line numbers for the NEW version of the file) and a short description of a code-review finding about that file. Determine whether the description clearly corresponds to a specific, identifiable line or contiguous range of lines in the NEW version of the file shown in the diff. If it does, report found=true with the 1-based startLine/endLine (inclusive) of that range, using the exact line numbers shown in the diff's own hunk headers/context. If the description does not clearly correspond to any specific line in this diff -- it is too vague, or the diff genuinely does not contain anything matching it -- report found=false and omit startLine/endLine (or set them to 0). Never guess a plausible-sounding line number you are not confident about; a wrong answer is worse than reporting not found."

// buildUserMessage renders the one user-turn message a relocation call
// sends -- filePath/snippet/diff, each clearly labeled, delimited exactly
// like every other untrusted/contextual block this codebase renders
// (§5.2) since diff/snippet content ultimately originates from a PR
// diff/an agent's own finding text, neither of which this package
// authored.
func buildUserMessage(filePath, snippet, diff string) string {
	return "File: " + filePath + "\n\n" +
		"Finding description:\n<finding_description>\n" + snippet + "\n</finding_description>\n\n" +
		"Diff (unified format, new-file line numbers per its own hunk headers):\n<file_diff>\n" + diff + "\n</file_diff>\n"
}

// buildResponseSchema constructs the JSON Schema every relocation call
// constrains its structured output to -- mirrors intentclassifier's own
// buildResponseSchema shape exactly (a flat object, a closed set of
// properties, additionalProperties: false).
func buildResponseSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"found": map[string]any{
				"type":        "boolean",
				"description": "Whether the finding description clearly corresponds to a specific line range in this diff's own new-file content.",
			},
			"startLine": map[string]any{
				"type":        "integer",
				"description": "1-based, inclusive start line in the diff's own new-file numbering. Required when found is true; ignored otherwise.",
			},
			"endLine": map[string]any{
				"type":        "integer",
				"description": "1-based, inclusive end line in the diff's own new-file numbering (equal to startLine for a single-line match). Required when found is true; ignored otherwise.",
			},
		},
		"required":             []string{"found", "startLine", "endLine"},
		"additionalProperties": false,
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		// schema is a fixed, hand-written literal with no dynamic content
		// -- a marshal failure here would mean this package itself no
		// longer compiles the intended shape, mirroring intentclassifier's
		// own identical panic-on-package-bug precedent (schema.go).
		panic("findingposition: buildResponseSchema: " + err.Error())
	}
	return raw
}

// responseSchema is built exactly once, at package init -- every
// relocation call shares this SAME schema value, never rebuilt per call
// (mirrors intentclassifier's own identical package-level responseSchema
// precedent).
var responseSchema = buildResponseSchema()

// structuredOutput is the shape Complete's raw JSON response unmarshals
// into -- the wire mirror of responseSchema's own required properties.
type structuredOutput struct {
	Found     bool `json:"found"`
	StartLine int  `json:"startLine"`
	EndLine   int  `json:"endLine"`
}

// valid reports whether s is a well-formed, USABLE relocation result --
// defense-in-depth against a provider that (despite the schema) returns
// something nonsensical, treated as a failure (lands on 0, 0) rather than
// trusted blindly, mirroring intentclassifier's own structuredOutput.valid
// precedent. found=false is always valid regardless of the two line
// fields (the model is reporting "not found", any StartLine/EndLine value
// alongside that is simply unused). found=true additionally requires
// StartLine >= 1, EndLine >= StartLine -- a genuine result can never claim
// line 0 (0 is THIS package's own "not found" sentinel, never a value a
// found=true result may legitimately report) or an inverted/negative
// range.
func (s structuredOutput) valid() bool {
	if !s.Found {
		return true
	}
	return s.StartLine >= 1 && s.EndLine >= s.StartLine
}
