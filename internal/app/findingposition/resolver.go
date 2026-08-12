package findingposition

import (
	"context"
	"encoding/json"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// Resolver is the real relocation-fallback implementation (§22.1.1),
// wrapping an already-constructed ports.LLM -- deliberately the SAME
// client instance/config internal/app/intentclassifier already
// constructs and uses (cmd/control-plane/main.go), never a second,
// independently-configured adapter: §22.1.1's own "reuses the existing
// LLM port, never a new call path" instruction is read here as reusing
// the concrete call path (client, provider, model), not merely the Go
// interface type.
type Resolver struct {
	llm      ports.LLM
	provider string
	model    string
}

// New builds a Resolver. provider/model are the SAME
// platform.Config.IntentClassifierProvider/Model values intentclassifier.
// New already receives -- a deliberate reuse, not a second, independently
// -tuned model choice: both are small, structured, non-agentic utility
// calls (§22.1.1's own "the same class of call as classification"), so
// there is no reason for this Step to introduce a second provider/model
// configuration surface for what is, functionally, the identical KIND of
// call.
func New(llmClient ports.LLM, provider, model string) *Resolver {
	return &Resolver{llm: llmClient, provider: provider, model: model}
}

// Resolve is §22.1.1's own relocation call: given the SAME (filePath,
// snippet, diff) reviewpost.MatchPosition already failed to anchor, it
// asks the underlying LLM, once, non-agentically (no tool access -- see
// doc.go), whether it can identify a line range. NEVER returns an error
// the caller must handle -- every failure (a *ports.LLMError of any Code,
// malformed/invalid output, or the model itself reporting found=false)
// degrades to (0, 0), mirroring intentclassifier.Service.Classify's own
// never-caller-fatal contract to the letter, and matching §22.1.1's own
// explicit "on failure... the finding stays unanchored (0, per above),
// never a second guess stacked on the first".
//
// Deliberately makes NO context.WithTimeout wrap of its own around the
// s.llm.Complete call -- ports.LLM's own doc comment (llm.go) is explicit
// that implementations rely on their own underlying SDK client's
// configured request-timeout option, and a caller racing a second,
// manually-armed timeout against it would never actually fire first
// (§18.1, the exact discipline intentclassifier.Service.Classify already
// follows for the identical reason).
func (r *Resolver) Resolve(ctx context.Context, filePath, snippet, diff string) (startLine, endLine int) {
	logger := platform.Logger(ctx)

	completion, err := r.llm.Complete(ctx, ports.CompletionRequest{
		Provider:       r.provider,
		Model:          r.model,
		System:         relocationSystemPrompt,
		Messages:       []ports.CompletionMessage{{Role: "user", Content: buildUserMessage(filePath, snippet, diff)}},
		ResponseSchema: responseSchema,
	})
	if err != nil {
		logger.Warn("findingposition: relocation llm call failed, leaving finding unanchored",
			"file_path", filePath, "error", err)
		return 0, 0
	}

	var parsed structuredOutput
	if unmarshalErr := json.Unmarshal(completion.Raw, &parsed); unmarshalErr != nil || !parsed.valid() {
		logger.Warn("findingposition: relocation llm returned invalid output, leaving finding unanchored",
			"file_path", filePath, "unmarshal_error", unmarshalErr, "raw_output", string(completion.Raw))
		return 0, 0
	}

	if !parsed.Found {
		logger.Info("findingposition: relocation llm reported not found, leaving finding unanchored", "file_path", filePath)
		return 0, 0
	}

	return parsed.StartLine, parsed.EndLine
}
