package automation

import (
	"fmt"

	"github.com/narvidev/narvi/internal/domain/environment"
)

// SandboxSettings is §8.4's own "sandboxSettings honored on automation
// sessions": the EXACT SAME two attributes internal/domain/environment.
// Environment (path_scope) and its own MockConfigured/ContractsPath
// (persisted on the Postgres environments row, never modeled as a domain
// type of their own -- see environment/doc.go's own doc comment) already
// carry for an ordinary web session, just namespaced onto an automation
// directly rather than requiring a separately-managed Environment row to
// reference by id (no such standalone create/list/update-by-id surface
// exists anywhere in this codebase yet, migrations/000021_environments.
// up.sql's own scope note, still true here).
//
// A zero-value SandboxSettings (nil PathScope, MockConfigured false, empty
// ContractsPath) means exactly what an absent pathScope/mockConfig means on
// restdtos.CreateSessionRequest today: no environments row is created for
// runs this automation fans out, and its own sessions stay fully unscoped
// -- unchanged behavior for every automation that never sets these,
// matching this codebase's own "absent = today's exact behavior" precedent
// (environment.Environment's own PathScope doc comment).
type SandboxSettings struct {
	// PathScope mirrors environment.Environment.PathScope exactly -- nil or
	// empty means unscoped.
	PathScope []string
	// MockConfigured mirrors restdtos.CreateSessionRequest.mockConfig's own
	// "presence of this key means true" convention, collapsed here into a
	// plain bool: true means an environments row is created for this
	// automation's own runs with mock_configured=true (and ContractsPath
	// below applied), exactly like CreateSessionOnTx's own hasMockConfig
	// branch.
	MockConfigured bool
	// ContractsPath mirrors environment.Environment's own contracts_path
	// (via restdtos.CreateSessionRequestMockConfig.ContractsPath) --
	// meaningful only when MockConfigured is true; "" then means "use the
	// default contracts/api", the SAME convention CreateSessionOnTx's own
	// caller (httpapi.CreateSession) already establishes. Ignored entirely
	// when MockConfigured is false.
	ContractsPath string
}

// IsUnscoped reports whether s carries no sandbox scoping at all -- neither
// a non-empty PathScope nor MockConfigured -- the single decision point
// app/automation's own fanout.go uses to decide whether to build a
// CreateSessionRequest with pathScope/mockConfig populated at all, mirrors
// CreateSessionOnTx's own identical "hasPathScope || hasMockConfig" gate,
// deliberately restated here as ITS OWN named predicate (not imported from
// httpapi, which this domain package must never depend on, §11) rather
// than inlined at every call site.
func IsUnscoped(s SandboxSettings) bool {
	return len(s.PathScope) == 0 && !s.MockConfigured
}

// ValidateSandboxSettings validates a candidate SandboxSettings before it is
// accepted onto an automation, at creation/update time -- reuses
// environment.ValidatePathScope/ValidateContractsPath directly (the SAME
// two functions httpapi.validateCreateSessionRequest already calls for an
// ordinary session's own pathScope/mockConfig.contractsPath) rather than
// re-implementing an independent, and possibly divergent, second copy of
// either check -- mirrors run.go's own precedent of this package importing
// a sibling domain package (internal/domain/turn) when the SAME decision
// genuinely applies unchanged.
//
// ContractsPath is validated only when MockConfigured is true and
// ContractsPath is non-empty (empty means "use the default", never
// validated -- matching ValidateContractsPath's own "empty is invalid
// input" contract, which would otherwise wrongly reject the "use the
// default" sentinel itself).
func ValidateSandboxSettings(s SandboxSettings) error {
	if len(s.PathScope) > 0 {
		if err := environment.ValidatePathScope(s.PathScope); err != nil {
			return fmt.Errorf("automation: invalid sandbox settings: %w", err)
		}
	}
	if s.MockConfigured && s.ContractsPath != "" {
		if err := environment.ValidateContractsPath(s.ContractsPath); err != nil {
			return fmt.Errorf("automation: invalid sandbox settings: %w", err)
		}
	}
	return nil
}
