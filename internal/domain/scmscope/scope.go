// Package scmscope implements §30.4(4)'s own pure decision: "scope
// introspection, fail-closed, at boot and at mint." Nothing before this
// Step validated any GitHub credential's own scope at all -- an operator
// pasting a classic, broadly-`repo`-scoped credential into the shadow
// slot would have silently re-armed every shadow sandbox with a
// write-capable token. This package is the one place that question is
// answered, shared byte-for-byte between the two call sites §30.4(4)
// names: cmd/control-plane's own boot-time check (the configured GitHub
// App's own granted permissions, read once via GET /app) and
// internal/adapters/inbound/httpapi.ScmCredentials' own per-mint check
// (the permissions actually granted on THIS installation access token,
// read back from the mint response).
//
// Pure: no I/O, no clock, no randomness (CLAUDE.md: "no I/O ... in
// /internal/domain"). Both call sites do their own GitHub HTTP call
// first and hand this package only the already-decoded permissions map
// GitHub returned.
package scmscope

import "fmt"

// permissionRead is the only permission LEVEL this package ever accepts,
// for any permission NAME. GitHub's own API spells a permission's level
// as one of "read", "write", or "admin" (never a boolean) -- see
// https://docs.github.com/en/rest/apps/apps and the "permissions" object
// on both a GitHub App's own /app response and an installation access
// token's own mint response.
const permissionRead = "read"

// NotReadOnlyError is returned by ValidateReadOnly when at least one
// permission in the map carries a level other than "read" -- Permission
// names which one, Level names what it actually was, so an operator (or
// this package's own caller, composing the boot-refusal message) can
// report the ONE offending permission rather than a generic "not
// read-only".
type NotReadOnlyError struct {
	Permission string
	Level      string
}

func (e *NotReadOnlyError) Error() string {
	return fmt.Sprintf("scmscope: permission %q is %q, not read-only", e.Permission, e.Level)
}

// EmptyPermissionsError is returned by ValidateReadOnly when permissions
// is empty. An App/installation granting NO permissions at all cannot
// clone anything either, so this is not the write-capable danger
// NotReadOnlyError guards against -- but it is not a usable read-only
// credential either, and treating "nothing to check" as "trivially
// read-only" would let a genuinely misconfigured App pass this check by
// accident (e.g. every permission entry silently dropped by an upstream
// bug) rather than failing loudly on the one signal available.
type EmptyPermissionsError struct{}

func (e *EmptyPermissionsError) Error() string {
	return "scmscope: no permissions were granted at all -- refusing to treat an empty permission set as read-only"
}

// ValidateReadOnly reports whether permissions -- a GitHub App/
// installation's own {permission-name: level} map, exactly as GitHub's
// API returns it -- grants nothing beyond read access. Fail-closed by
// construction: an unrecognized level, a "write"/"admin" level on ANY
// permission (not merely "contents" or "metadata" -- the two this Step's
// own mint requests), or an empty map are all refused. There is
// deliberately no allowlist of "permissions we don't care about": a
// permission this package has never heard of, granted at any level other
// than "read", still fails the check, because the danger this function
// exists to catch is exactly a permission nobody anticipated.
func ValidateReadOnly(permissions map[string]string) error {
	if len(permissions) == 0 {
		return &EmptyPermissionsError{}
	}
	for name, level := range permissions {
		if level != permissionRead {
			return &NotReadOnlyError{Permission: name, Level: level}
		}
	}
	return nil
}
