package cloudidentity_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
)

// TestReservedEnvVarNames_ContainsEveryDocumentedName pins the exact 7
// fixed AWS/GCP/Azure env-var names §27.3 documents -- deliberately NOT
// including a generic binding's own customer-chosen env-var name (see
// envvars.go's own top doc comment for why that one cannot be a fixed
// reserved literal).
func TestReservedEnvVarNames_ContainsEveryDocumentedName(t *testing.T) {
	want := map[string]bool{
		"AWS_WEB_IDENTITY_TOKEN_FILE":    true,
		"AWS_ROLE_ARN":                   true,
		"AWS_ROLE_SESSION_NAME":          true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
		"AZURE_FEDERATED_TOKEN_FILE":     true,
		"AZURE_CLIENT_ID":                true,
		"AZURE_TENANT_ID":                true,
	}
	got := cloudidentity.ReservedEnvVarNames()
	if len(got) != len(want) {
		t.Fatalf("ReservedEnvVarNames() = %v (len %d), want %d entries", got, len(got), len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("ReservedEnvVarNames() contains unexpected name %q", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("ReservedEnvVarNames() is missing: %v", want)
	}
}

// TestReservedEnvVarNames_DefensiveCopy proves mutating the returned slice
// never corrupts a later call's own result -- mirrors
// providercredential.EnvVarNames' own identical defensive-copy guarantee.
func TestReservedEnvVarNames_DefensiveCopy(t *testing.T) {
	first := cloudidentity.ReservedEnvVarNames()
	first[0] = "CORRUPTED"
	second := cloudidentity.ReservedEnvVarNames()
	if second[0] == "CORRUPTED" {
		t.Errorf("ReservedEnvVarNames() mutated by a prior caller's own slice")
	}
}

// TestEnvVarConstants_MatchDocumentedLiterals pins each exported constant
// to the exact literal §27.3 documents -- a typo in any one of these would
// silently reserve/inject the WRONG name.
func TestEnvVarConstants_MatchDocumentedLiterals(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{cloudidentity.EnvVarAWSWebIdentityTokenFile, "AWS_WEB_IDENTITY_TOKEN_FILE"},
		{cloudidentity.EnvVarAWSRoleARN, "AWS_ROLE_ARN"},
		{cloudidentity.EnvVarAWSRoleSessionName, "AWS_ROLE_SESSION_NAME"},
		{cloudidentity.EnvVarGoogleApplicationCredentials, "GOOGLE_APPLICATION_CREDENTIALS"},
		{cloudidentity.EnvVarAzureFederatedTokenFile, "AZURE_FEDERATED_TOKEN_FILE"},
		{cloudidentity.EnvVarAzureClientID, "AZURE_CLIENT_ID"},
		{cloudidentity.EnvVarAzureTenantID, "AZURE_TENANT_ID"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("constant = %q, want %q", tc.got, tc.want)
		}
	}
}
