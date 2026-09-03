package cloudidentity

import "github.com/narvidev/narvi/internal/domain/providercredential"

// BindingScopes is the restricted set of internal/domain/providercredential
// .Scope values a cloud_identity_bindings row may ever declare --
// providercredential.ScopeEnvironment and providercredential.ScopeGlobal
// ONLY, never Repo/User/Automation (§27.3: "Deliberately no repo scope: a
// deployment target is an Environment property... not a repo property" --
// and neither a user nor an automation is ever a deployment target at
// all). This package deliberately reuses providercredential's own Scope
// type and its Resolve function (internal/adapters/inbound/httpapi/
// cloudidentitytoken.go calls providercredential.Resolve directly over
// cloud_identity_bindings candidates) rather than inventing a second,
// parallel scope vocabulary -- see this package's own doc.go and
// providercredential's own scope.go doc comment (scopePriority: "environment
// -> repo -> global") for why reusing the SAME priority map is safe here:
// this table restricts itself to exactly the 2 scope values whose
// relative priority (environment=2 outranks global=4) that map already
// gets right, and Repo/User/Automation candidates are simply never
// constructed for this table, so their own priority ranking is never
// consulted.
var BindingScopes = []providercredential.Scope{providercredential.ScopeEnvironment, providercredential.ScopeGlobal}

// IsValidBindingScope reports whether s is one of the 2 scope values a
// cloud_identity_bindings row may declare -- a STRICTER check than
// providercredential.IsValidScope (which also accepts Repo/User/
// Automation, none of which apply to this table).
func IsValidBindingScope(s providercredential.Scope) bool {
	for _, allowed := range BindingScopes {
		if s == allowed {
			return true
		}
	}
	return false
}
