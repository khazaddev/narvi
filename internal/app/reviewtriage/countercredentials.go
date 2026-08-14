package reviewtriage

import (
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/providercredential"
)

// CredentialedProviders reduces rows (every provider_credentials candidate
// that could apply to one session -- ProviderCredentialStore.ListForResolution's
// own return shape, across all 4 scopes at once) into the set of provider
// names (lowercase, matching sqlcgen.ProviderCredentialProvider's own
// string values, e.g. "anthropic") this session has at least one
// RESOLVABLE candidate for -- ResolveCounterReviewerModel's own
// credentialedProviders input (B2 fix, §26.4/Step 69: "prefer no pin over
// guessing when the opposing provider is not known-credentialed").
//
// Existence only, never a resolved VALUE: this never decrypts anything and
// never even inspects ValueEncrypted -- it mirrors httpapi.
// ProviderCredentialsDelivery's own identical byProvider-then-Resolve
// grouping (providercredentialsdelivery.go), stopping one step earlier.
// That handler's own job is producing a usable credential VALUE for a
// sandbox to authenticate with; this function's only job is answering "if
// that handler ran right now for this same session, would it produce
// anything for provider X at all" -- reusing providercredential.Resolve
// itself (rather than a bespoke "is there any row" check) so the answer
// stays consistent with that handler's own "most specific wins, and an
// unrecognized Scope is silently ignored" rules, never a second,
// independently-derived notion of "credentialed".
func CredentialedProviders(rows []sqlcgen.ProviderCredential) map[string]bool {
	byProvider := make(map[string][]providercredential.Candidate[struct{}], len(rows))
	for _, row := range rows {
		provider := string(row.Provider)
		byProvider[provider] = append(byProvider[provider], providercredential.Candidate[struct{}]{
			Scope: providercredential.Scope(row.Scope),
		})
	}

	credentialed := make(map[string]bool, len(byProvider))
	for provider, candidates := range byProvider {
		if _, ok := providercredential.Resolve(candidates); ok {
			credentialed[provider] = true
		}
	}
	return credentialed
}
