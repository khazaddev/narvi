package contractstest

import (
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
)

func TestSessionConfigRoundTrip(t *testing.T) {
	// session-config.schema.json's root is a "$ref" to #/$defs/SessionConfig,
	// so compiling the document itself (no fragment) already validates the
	// full SESSION_CONFIG shape (§4.1: "sandbox env passed as one
	// SESSION_CONFIG JSON document").
	sch := compileSchema(t, "session-config/v1/session-config.schema.json", "")

	t.Run("WithCorrelationIdAndBranch", func(t *testing.T) {
		correlationID := "corr-123"
		branch := "main"
		roundTrip(t, sch, sessionconfig.SessionConfig{
			SessionId:         testSessionID,
			Gen:               1,
			SandboxToken:      "sandbox-token-plaintext",
			BootMode:          sessionconfig.SessionConfigBootModeFresh,
			ControlPlaneWsUrl: "wss://cp.narvi.dev/sessions/" + testSessionID + "/ws?type=sandbox",
			Repos: []sessionconfig.SessionConfigReposElem{
				{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: &branch},
			},
			CorrelationId: &correlationID,
		})
	})

	t.Run("NullCorrelationIdAndBranch", func(t *testing.T) {
		// correlationId null means no upstream correlation id exists; repo
		// branch null means create the session branch from the repo's
		// default base branch (§4.1/§3.4).
		roundTrip(t, sch, sessionconfig.SessionConfig{
			SessionId:         testSessionID,
			Gen:               2,
			SandboxToken:      "sandbox-token-plaintext",
			BootMode:          sessionconfig.SessionConfigBootModeSnapshotRestore,
			ControlPlaneWsUrl: "wss://cp.narvi.dev/sessions/" + testSessionID + "/ws?type=sandbox",
			Repos: []sessionconfig.SessionConfigReposElem{
				{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: nil},
			},
			CorrelationId: nil,
		})
	})
}
