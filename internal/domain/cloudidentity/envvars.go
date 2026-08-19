package cloudidentity

// This file (envvars.go) is Step 73b's own ("cloud identity: sandbox-side
// consumption + kubeconfig injection", §27.3) enumeration of the STANDARD,
// cloud-SDK-documented env-var names sandbox-agent's own in-sandbox
// consumption mechanism (cmd/sandbox-agent/cloudidentity.go) sets on every
// spawned process for a resolved binding -- §27.3, verbatim:
// "AWS_WEB_IDENTITY_TOKEN_FILE + AWS_ROLE_ARN (+ session name)... for
// AWS's AssumeRoleWithWebIdentity flow; GOOGLE_APPLICATION_CREDENTIALS
// pointing at a generated external-account credential-config JSON... for
// GCP's STS exchange; AZURE_FEDERATED_TOKEN_FILE + AZURE_CLIENT_ID +
// AZURE_TENANT_ID for Azure workload identity."
//
// Mirrors internal/domain/providercredential's own envVarNames/
// AllEnvVarNames precedent EXACTLY (a Kind-keyed map of fixed, external,
// SDK-documented names, flattened by an exported "every name across every
// Kind" function) -- reused here for the identical reason
// providercredential's own doc comment gives: internal/domain/
// sandboxsecret.ValidateName must reject every one of these outright, so
// that a maintainer holding ActionManageEnvSecrets can never define
// AWS_ROLE_ARN (or any of its siblings) as a sandbox secret and silently
// redirect this whole federation mechanism -- "every env-var name has
// exactly one owning mechanism" (§27.1's own rule, extended here to
// §27.3's injected names). ReservedEnvVarNames (below) is that SAME
// exported list; cmd/sandbox-agent/cloudidentity.go's own env-building
// code constructs its "NAME=VALUE" entries FROM these exact constants
// too, never a second, independently-typed literal -- so the reservation
// (sandboxsecret.ValidateName) and the injection (cloudidentity's own
// consumption code) can never drift apart, exactly like
// OpenCodeReservedPrefix's own "one exported source, two consumers"
// shape (internal/domain/sandboxsecret/name.go).
const (
	// EnvVarAWSWebIdentityTokenFile is the AWS SDK's own documented env
	// var naming the file AssumeRoleWithWebIdentity reads the OIDC token
	// from.
	EnvVarAWSWebIdentityTokenFile = "AWS_WEB_IDENTITY_TOKEN_FILE"
	// EnvVarAWSRoleARN is the AWS SDK's own documented env var naming the
	// role AssumeRoleWithWebIdentity assumes.
	EnvVarAWSRoleARN = "AWS_ROLE_ARN"
	// EnvVarAWSRoleSessionName is the AWS SDK's own documented, OPTIONAL
	// env var naming the assumed-role session -- §27.3's own parenthetical
	// "(+ session name)". Set by cloudidentity.go to a value derived from
	// this session's own id, for CloudTrail auditability, rather than left
	// for the AWS SDK to generate one on its own.
	EnvVarAWSRoleSessionName = "AWS_ROLE_SESSION_NAME"

	// EnvVarGoogleApplicationCredentials is the Google Cloud SDK's own
	// documented env var naming a credential-config JSON file -- here, a
	// GENERATED external-account document whose credential_source.file
	// points at the token file (§27.3), never a real service-account key.
	EnvVarGoogleApplicationCredentials = "GOOGLE_APPLICATION_CREDENTIALS"

	// EnvVarAzureFederatedTokenFile is the Azure Workload Identity SDK's
	// own documented env var naming the file the federated-token exchange
	// reads from.
	EnvVarAzureFederatedTokenFile = "AZURE_FEDERATED_TOKEN_FILE"
	// EnvVarAzureClientID is the Azure Workload Identity SDK's own
	// documented env var naming the federated credential's app/client id.
	EnvVarAzureClientID = "AZURE_CLIENT_ID"
	// EnvVarAzureTenantID is the Azure Workload Identity SDK's own
	// documented env var naming the federated credential's tenant id.
	EnvVarAzureTenantID = "AZURE_TENANT_ID"
)

// reservedEnvVarNames is every fixed (never customer-chosen) env-var name
// this package's own consumption mechanism ever sets -- deliberately NOT
// including the `generic` Kind's own env-var name, which is customer-
// configured PER BINDING (params' own "the env-var name to publish the
// token path under", cloud_identity_bindings.params) and therefore
// cannot be enumerated as a fixed reserved literal the way the other
// three clouds' SDK-mandated names can; a generic binding's own chosen
// name is validated the same way any OTHER sandbox_secrets name is
// (POSIX shape, not already reserved by a DIFFERENT mechanism) -- there
// is no separate reservation to make for a name this package does not
// itself pick.
var reservedEnvVarNames = []string{
	EnvVarAWSWebIdentityTokenFile,
	EnvVarAWSRoleARN,
	EnvVarAWSRoleSessionName,
	EnvVarGoogleApplicationCredentials,
	EnvVarAzureFederatedTokenFile,
	EnvVarAzureClientID,
	EnvVarAzureTenantID,
}

// ReservedEnvVarNames returns every env-var name this package's own
// AWS/GCP/Azure consumption mechanism sets -- the single, exported source
// internal/domain/sandboxsecret.ValidateName reserves FROM directly (see
// this file's own top doc comment). A defensive copy, mirroring
// providercredential.EnvVarNames' own identical "callers may append,
// never mutate the shared backing array" discipline.
func ReservedEnvVarNames() []string {
	out := make([]string, len(reservedEnvVarNames))
	copy(out, reservedEnvVarNames)
	return out
}
