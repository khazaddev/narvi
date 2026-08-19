package cloudidentity

import (
	"errors"
	"fmt"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/providercredential"
)

// ErrInvalidKind is returned by ValidateBinding for a kind
// IsValidKind rejects.
var ErrInvalidKind = errors.New("cloudidentity: invalid kind")

// ErrInvalidScope is returned by ValidateBinding for a scope
// IsValidBindingScope rejects.
var ErrInvalidScope = errors.New("cloudidentity: invalid scope for a cloud identity binding")

// ErrAzureGlobalScopeForbidden is returned by ValidateBinding for the one
// structurally-forbidden (kind, scope) combination -- kind=azure at
// scope=global. See this package's own doc.go for the full "why" (gap 3
// of this Step's own brief).
var ErrAzureGlobalScopeForbidden = errors.New("cloudidentity: an azure binding must be environment-scoped -- azure federated-credential matching is exact-match only on sub, which is per-Environment, so a global-scope azure binding cannot honestly trust every Environment from a single cloud-side federated credential")

// ErrBlankAudience is returned by ValidateAudience for an empty or
// all-whitespace value.
var ErrBlankAudience = errors.New("cloudidentity: audience must not be blank")

// ErrAudienceContainsNUL is returned by ValidateAudience for a value
// containing an embedded NUL byte (U+0000) -- mirrors provider_
// credentials' own containsNULByte precedent (internal/adapters/inbound/
// httpapi/providercredentials.go): a NUL-bearing string breaks assumptions
// downstream tooling and logging make about ordinary text, even though
// (unlike a provider-credential value) an audience string is never
// written into a spawned process's own os/exec environment.
var ErrAudienceContainsNUL = errors.New("cloudidentity: audience must not contain a NUL byte")

// ValidateBinding checks a cloud_identity_bindings row's own (kind, scope)
// pair for structural validity, BEFORE it ever reaches storage: kind must
// be one of the 4 recognized values (IsValidKind), scope must be one of
// the 2 a binding may declare (IsValidBindingScope), and kind=azure may
// never pair with scope=global (ErrAzureGlobalScopeForbidden -- this
// Step's own gap-3 resolution, see doc.go). Mirrored, redundantly, by
// migrations/000093_cloud_identity_bindings.up.sql's own CHECK
// constraints for defense in depth -- this function is what lets the
// httpapi layer reject a bad request with a clear 400 before ever issuing
// the INSERT/UPDATE that constraint would otherwise catch as an opaque
// Postgres error.
func ValidateBinding(kind Kind, scope providercredential.Scope) error {
	if !IsValidKind(kind) {
		return fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
	if !IsValidBindingScope(scope) {
		return fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
	if kind == KindAzure && scope == providercredential.ScopeGlobal {
		return ErrAzureGlobalScopeForbidden
	}
	return nil
}

// ValidateAudience checks an `aud` value a binding declares (or a caller
// requests at minting time) for basic shape: non-blank, no embedded NUL
// byte. Deliberately does NOT validate against any per-cloud expected
// literal (sts.amazonaws.com, a GCP resource name, api://
// AzureADTokenExchange, or anything a generic consumer expects) -- §27.3
// is explicit that audience is customer-set, free-form text the cloud
// itself documents, not a closed vocabulary this codebase could enumerate
// honestly.
func ValidateAudience(audience string) error {
	if strings.TrimSpace(audience) == "" {
		return ErrBlankAudience
	}
	if strings.ContainsRune(audience, 0) {
		return ErrAudienceContainsNUL
	}
	return nil
}
