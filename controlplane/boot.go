package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/narvidev/narvi/internal/domain/scmscope"
)

// appPermissionsChecker is the narrow slice of *githubapp.Client
// verifyGitHubAppScopeAtBoot actually needs -- an interface so this
// function is unit-testable against a fake, without a real GitHub App or
// network access (see internal/adapters/outbound/githubapp's own doc.go
// for why no real one is reachable from this environment at all).
type appPermissionsChecker interface {
	AppPermissions(ctx context.Context) (map[string]string, error)
}

// verifyGitHubAppScopeAtBoot is §30.4(4)'s own boot-time half of "scope
// introspection, fail-closed, at boot and at mint": before this process
// ever starts serving traffic, confirm the configured GitHub App's own
// maximum granted permissions are read-only (internal/domain/scmscope.
// ValidateReadOnly). An operator who pastes a broad App (or misconfigures
// its own permissions to include Contents: Read & write) into this slot
// gets a loud boot refusal here, never a silent re-arming of every shadow
// sandbox on the first real mint. The refusal message says what to fix,
// not which section requires it -- an operator reading this output has no
// reason to know or care about this codebase's own internal section
// numbers.
func verifyGitHubAppScopeAtBoot(ctx context.Context, client appPermissionsChecker, timeout time.Duration) error {
	scopeCheckCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	appPermissions, err := client.AppPermissions(scopeCheckCtx)
	if err != nil {
		return fmt.Errorf("check the configured GitHub App's own granted permissions at boot: %w", err)
	}
	if err := scmscope.ValidateReadOnly(appPermissions); err != nil {
		return fmt.Errorf(
			"refusing to start: the configured GitHub App grants more than read access (%w) -- narrow its own permissions to Contents: Read-only and Metadata: Read-only in the App's settings on GitHub, then restart",
			err,
		)
	}
	return nil
}
