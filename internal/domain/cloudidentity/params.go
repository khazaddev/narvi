package cloudidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This file (params.go) is Step 73b's own ("cloud identity: sandbox-side
// consumption + kubeconfig injection", §27.3) typed vocabulary for a
// cloud_identity_bindings row's own per-Kind params shape -- §27.3's own
// migration comment (000093_cloud_identity_bindings.up.sql) names each
// kind's own identifiers in prose ("AWS: role ARN; GCP: workload-
// identity-provider resource name + optional service-account email;
// Azure: client id + tenant id; generic: the env-var name to publish the
// token path under"); this file gives that prose an actual, parseable
// shape and the ONE place ValidateParams enforces it -- consumed by
// cmd/sandbox-agent/cloudidentity.go (the ONLY consumer this Step ships;
// 73a's own CRUD, internal/adapters/inbound/httpapi/cloudidentitybindings.go,
// deliberately stores params as an opaque, unvalidated-beyond-"is it a
// JSON object" blob per that file's own doc comment -- unchanged here).
// Mirrors internal/domain/clusterbinding's own CloudParams/OIDCParams/
// StaticParams + ValidateParams shape exactly.

// AWSParams is KindAWS's own params shape.
type AWSParams struct {
	RoleARN string `json:"roleArn"`
}

// GCPParams is KindGCP's own params shape. ServiceAccountEmail is
// OPTIONAL -- §27.3's own "workload-identity-provider resource name +
// OPTIONAL service-account email" -- present only when the workload
// identity pool itself is not already bound 1:1 to a single service
// account (GCP's own "service account impersonation" variant of workload
// identity federation).
type GCPParams struct {
	WorkloadIdentityProvider string `json:"workloadIdentityProvider"`
	ServiceAccountEmail      string `json:"serviceAccountEmail,omitempty"`
}

// AzureParams is KindAzure's own params shape.
type AzureParams struct {
	ClientID string `json:"clientId"`
	TenantID string `json:"tenantId"`
}

// GenericParams is KindGeneric's own params shape -- §27.3's own "the
// env-var name to publish the token path under". ValidateParams (below)
// only confirms EnvVar is non-blank -- it does NOT re-run
// internal/domain/sandboxsecret.ValidateName's own POSIX-shape-and-not-
// already-reserved rule here, because sandboxsecret already imports THIS
// package (ReservedEnvVarNames, envvars.go) to build its own reservation
// list, and this package importing sandboxsecret back would be a direct
// import cycle. cmd/sandbox-agent/cloudidentity.go (this Step's own sole
// consumer of GenericParams) runs that fuller check itself, at the point
// of injection, where importing sandboxsecret is safe -- see that file's
// own doc comment.
type GenericParams struct {
	EnvVar string `json:"envVar"`
}

// Sentinel errors ValidateParams returns, wrapped via fmt.Errorf("%w: ...")
// -- mirrors internal/domain/clusterbinding's own identical sentinel-error
// precedent (params.go).
var (
	// ErrParamsMalformed means params does not even parse as a JSON
	// object of the shape kind's own consumption mechanism expects.
	ErrParamsMalformed = errors.New("cloudidentity: params malformed for this kind")
	// ErrRoleARNRequired means KindAWS's params.roleArn is blank.
	ErrRoleARNRequired = errors.New("cloudidentity: params.roleArn is required for the aws kind")
	// ErrWorkloadIdentityProviderRequired means KindGCP's
	// params.workloadIdentityProvider is blank.
	ErrWorkloadIdentityProviderRequired = errors.New("cloudidentity: params.workloadIdentityProvider is required for the gcp kind")
	// ErrClientIDRequired means KindAzure's params.clientId is blank.
	ErrClientIDRequired = errors.New("cloudidentity: params.clientId is required for the azure kind")
	// ErrTenantIDRequired means KindAzure's params.tenantId is blank.
	ErrTenantIDRequired = errors.New("cloudidentity: params.tenantId is required for the azure kind")
	// ErrEnvVarRequired means KindGeneric's params.envVar is blank. See
	// GenericParams' own doc comment for why the FULLER injectable-name
	// check (sandboxsecret.ValidateName) is not run here.
	ErrEnvVarRequired = errors.New("cloudidentity: params.envVar is required for the generic kind")
)

// ValidateParams checks params against the shape kind's own consumption
// mechanism requires (AWSParams/GCPParams/AzureParams/GenericParams,
// above) -- called by cmd/sandbox-agent/cloudidentity.go BEFORE it ever
// trusts a delivered binding's own params enough to write a token
// file/build an env var from them. Pure -- no I/O, no time.Now(), no
// randomness (§11); raw is already-decoded JSON bytes, never read from
// disk/network by this function itself.
func ValidateParams(kind Kind, raw json.RawMessage) error {
	switch kind {
	case KindAWS:
		var p AWSParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.RoleARN) == "" {
			return ErrRoleARNRequired
		}
		return nil
	case KindGCP:
		var p GCPParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.WorkloadIdentityProvider) == "" {
			return ErrWorkloadIdentityProviderRequired
		}
		return nil
	case KindAzure:
		var p AzureParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.ClientID) == "" {
			return ErrClientIDRequired
		}
		if strings.TrimSpace(p.TenantID) == "" {
			return ErrTenantIDRequired
		}
		return nil
	case KindGeneric:
		var p GenericParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrParamsMalformed, err)
		}
		if strings.TrimSpace(p.EnvVar) == "" {
			return ErrEnvVarRequired
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
}
