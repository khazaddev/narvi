package clusterbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
)

// CloudParams is AuthKindCloud's own params shape -- §27.4: "which of the
// three [clouds]" the rendered kubeconfig's exec stanza targets, plus an
// optional region AWS's own `aws eks get-token --region` flag can use.
// Cloud reuses internal/domain/cloudidentity's own Kind STRING VALUES
// (KindAWS/KindGCP/KindAzure) for its 3 recognized literals -- never
// KindGeneric, which names no real cloud/exec-plugin at all -- so the two
// packages' own vocabularies for "which cloud" can never drift into two
// different spellings of the same thing.
type CloudParams struct {
	Cloud  string `json:"cloud"`
	Region string `json:"region,omitempty"`
}

// OIDCParams is AuthKindOIDC's own params shape -- the audience/client id
// kube-apiserver's own --oidc-client-id trusts, and the exact value
// cmd/sandbox-agent's kube-credential subcommand requests a §27.3 token
// for.
type OIDCParams struct {
	ClientID string `json:"clientId"`
}

// StaticParams is AuthKindStatic's own params shape -- the sandbox_secrets
// (Step 72) NAME whose resolved value is the complete, already-usable
// kubeconfig file content, written to disk verbatim (§27.4: "never
// env-var-expanded").
type StaticParams struct {
	SecretName string `json:"secretName"`
}

// validCloudValues is CloudParams.Cloud's own closed vocabulary -- the 3
// REAL clouds §27.4 names exec-credential plugins for
// (`aws eks get-token` / `gke-gcloud-auth-plugin` / `kubelogin`).
// cloudidentity.KindGeneric is deliberately excluded: there is no
// "generic" Kubernetes exec-credential plugin to invoke, unlike §27.3's
// own generic escape hatch for an arbitrary JWT-federating STS consumer.
var validCloudValues = map[string]bool{
	string(cloudidentity.KindAWS):   true,
	string(cloudidentity.KindGCP):   true,
	string(cloudidentity.KindAzure): true,
}

// Sentinel errors ValidateParams returns, wrapped via fmt.Errorf("%w: ...")
// so a caller can branch on the reason while a human-facing message still
// names the offending value -- mirrors internal/domain/cloudidentity's own
// established sentinel-error precedent (validate.go).
var (
	// ErrParamsMalformed means params does not even parse as a JSON
	// object of the shape authKind's own rung expects.
	ErrParamsMalformed = errors.New("clusterbinding: params malformed for this auth kind")
	// ErrCloudRequired means AuthKindCloud's params.cloud is blank or not
	// one of the 3 recognized cloud values.
	ErrCloudRequired = errors.New("clusterbinding: params.cloud must be one of aws, gcp, azure")
	// ErrClientIDRequired means AuthKindOIDC's params.clientId is blank.
	ErrClientIDRequired = errors.New("clusterbinding: params.clientId is required for the oidc auth rung")
	// ErrSecretNameRequired means AuthKindStatic's params.secretName is
	// blank.
	ErrSecretNameRequired = errors.New("clusterbinding: params.secretName is required for the static auth rung")
)

// ValidateParams checks params against the shape authKind's own rung
// requires (CloudParams/OIDCParams/StaticParams, above) -- called AFTER
// Validate has already confirmed authKind itself is recognized. Pure --
// no I/O, no time.Now(), no randomness (§11); raw is already-decoded JSON
// bytes, never read from disk/network by this function itself.
func ValidateParams(authKind AuthKind, raw json.RawMessage) error {
	switch authKind {
	case AuthKindCloud:
		var p CloudParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if !validCloudValues[p.Cloud] {
			return fmt.Errorf("%w: %q", ErrCloudRequired, p.Cloud)
		}
		return nil
	case AuthKindOIDC:
		var p OIDCParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.ClientID) == "" {
			return ErrClientIDRequired
		}
		return nil
	case AuthKindStatic:
		var p StaticParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.SecretName) == "" {
			return ErrSecretNameRequired
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAuthKind, authKind)
	}
}
