package clusterbinding

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNameBlank is returned by Validate for an empty or all-whitespace
// name.
var ErrNameBlank = errors.New("clusterbinding: name must not be blank")

// ErrInvalidAuthKind is returned by Validate for an authKind
// IsValidAuthKind rejects.
var ErrInvalidAuthKind = errors.New("clusterbinding: invalid auth kind")

// ErrServerURLRequired is returned by Validate when authKind requires a
// rendered `cluster` stanza (cloud/oidc) but serverURL is blank.
var ErrServerURLRequired = errors.New("clusterbinding: serverUrl is required for the cloud/oidc auth rungs")

// ErrCABundleRequired is returned by Validate when authKind requires a
// rendered `cluster` stanza (cloud/oidc) but caBundle is blank.
var ErrCABundleRequired = errors.New("clusterbinding: caBundle is required for the cloud/oidc auth rungs")

// Validate checks a cluster_bindings row's own structural fields BEFORE it
// ever reaches storage -- name non-blank, authKind one of the 3 recognized
// values, and serverURL/caBundle both present for the two rungs that
// render a kubeconfig `cluster` stanza from them (cloud/oidc) -- mirrored,
// redundantly, by migrations/000094_cluster_bindings.up.sql's own CHECK
// constraints for defense in depth, exactly like internal/domain/
// cloudidentity.ValidateBinding's own pairing with that table's CHECK
// constraints. AuthKindStatic is deliberately exempt from the
// serverURL/caBundle requirement -- see this package's own doc.go for why
// (the referenced sandbox_secrets document already carries its own).
func Validate(name string, authKind AuthKind, serverURL, caBundle string) error {
	if strings.TrimSpace(name) == "" {
		return ErrNameBlank
	}
	if !IsValidAuthKind(authKind) {
		return fmt.Errorf("%w: %q", ErrInvalidAuthKind, authKind)
	}
	if authKind == AuthKindStatic {
		return nil
	}
	if strings.TrimSpace(serverURL) == "" {
		return ErrServerURLRequired
	}
	if strings.TrimSpace(caBundle) == "" {
		return ErrCABundleRequired
	}
	return nil
}
