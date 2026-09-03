package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/narvidev/narvi/internal/app/ports"
)

// ProviderAnthropic is this package's own provider name -- the value
// callers set NARVI_INTENT_CLASSIFIER_PROVIDER to select the real
// Anthropic adapter (Config.Provider / ports.CompletionRequest.Provider).
const ProviderAnthropic = "anthropic"

// Config is what New needs to resolve and construct a ports.LLM.
// Deliberately holds no Model field: ports.CompletionRequest.Model is a
// genuine PER-CALL parameter (§4.3: this port is shaped for reuse by the
// future model catalog/code-review work too, which may pick a different
// model per call through the SAME client) -- Config only carries
// provider-LEVEL config (credentials, transport).
type Config struct {
	// Provider selects which concrete implementation New returns (today:
	// only ProviderAnthropic is real). An unrecognized value is a real,
	// reachable runtime path -- see unsupportedAdapter below -- never a
	// construction-time error.
	Provider string
	// APIKey is the Anthropic API key. Empty means every Complete call on
	// the constructed adapter deterministically fails with
	// ports.CodeNoAPIKey (§18.1) -- in real production wiring this never
	// happens, since platform.Config.Load requires NARVI_ANTHROPIC_API_KEY
	// non-empty at boot; this is what makes CodeNoAPIKey a REAL, testable
	// path rather than dead code.
	APIKey string
	// BaseURL overrides the Anthropic API's own default base URL --
	// empty means the SDK's real default. Tests point this at an
	// httptest.Server.
	BaseURL string
	// Timeout is platform.Timeouts.IntentClassifierLLMTimeout, configured
	// directly on the underlying SDK client (never raced against a
	// second, manually-armed context.WithTimeout -- §18.1).
	Timeout time.Duration
}

// New resolves cfg.Provider against this codebase's own small provider
// registry -- the wiring-layer factory §8's own "multi-provider is an
// architectural requirement" section calls for. NEVER fails to construct:
// an unrecognized Provider name returns a ports.LLM whose every Complete
// call deterministically returns
// *ports.LLMError{Code: ports.CodeUnsupportedProvider} instead (a real,
// reachable, always-fails-the-same-way value, not a silent substitution
// and not a process-boot crash over a misconfigured internal
// classification feature).
func New(cfg Config) ports.LLM {
	switch cfg.Provider {
	case ProviderAnthropic:
		return newAnthropicAdapter(cfg)
	default:
		return &unsupportedAdapter{provider: cfg.Provider}
	}
}

// unsupportedAdapter implements ports.LLM for an unrecognized
// CompletionRequest.Provider — every Complete call fails the SAME way,
// deterministically, forever.
type unsupportedAdapter struct {
	provider string
}

var _ ports.LLM = (*unsupportedAdapter)(nil)

func (a *unsupportedAdapter) Complete(_ context.Context, _ ports.CompletionRequest) (ports.CompletionResponse, error) {
	return ports.CompletionResponse{}, &ports.LLMError{
		Code:     ports.CodeUnsupportedProvider,
		Provider: a.provider,
		Err:      fmt.Errorf("llm: unsupported provider %q", a.provider),
	}
}
