package clusterbinding_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/clusterbinding"
)

func TestIsValidAuthKind(t *testing.T) {
	tests := []struct {
		name string
		k    clusterbinding.AuthKind
		want bool
	}{
		{"cloud", clusterbinding.AuthKindCloud, true},
		{"oidc", clusterbinding.AuthKindOIDC, true},
		{"static", clusterbinding.AuthKindStatic, true},
		{"empty", clusterbinding.AuthKind(""), false},
		{"unrecognized", clusterbinding.AuthKind("manual"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterbinding.IsValidAuthKind(tc.k); got != tc.want {
				t.Errorf("IsValidAuthKind(%q) = %v, want %v", tc.k, got, tc.want)
			}
		})
	}
}

func TestAllAuthKinds_EveryEntryIsValidAndInPreferenceOrder(t *testing.T) {
	want := []clusterbinding.AuthKind{clusterbinding.AuthKindCloud, clusterbinding.AuthKindOIDC, clusterbinding.AuthKindStatic}
	if len(clusterbinding.AllAuthKinds) != len(want) {
		t.Fatalf("len(AllAuthKinds) = %d, want %d", len(clusterbinding.AllAuthKinds), len(want))
	}
	for i, k := range clusterbinding.AllAuthKinds {
		if k != want[i] {
			t.Errorf("AllAuthKinds[%d] = %q, want %q (preference order: cloud > oidc > static)", i, k, want[i])
		}
		if !clusterbinding.IsValidAuthKind(k) {
			t.Errorf("AllAuthKinds contains %q, which IsValidAuthKind rejects", k)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		bindName  string
		authKind  clusterbinding.AuthKind
		serverURL string
		caBundle  string
		wantErr   error
	}{
		{"cloud with server+ca ok", "prod", clusterbinding.AuthKindCloud, "https://cluster.example", "-----BEGIN CERTIFICATE-----", nil},
		{"oidc with server+ca ok", "prod", clusterbinding.AuthKindOIDC, "https://cluster.example", "-----BEGIN CERTIFICATE-----", nil},
		{"static with no server/ca ok", "prod", clusterbinding.AuthKindStatic, "", "", nil},
		{"static with server/ca still ok (ignored, not rejected)", "prod", clusterbinding.AuthKindStatic, "https://cluster.example", "ca", nil},
		{"blank name", "", clusterbinding.AuthKindCloud, "https://cluster.example", "ca", clusterbinding.ErrNameBlank},
		{"whitespace-only name", "   ", clusterbinding.AuthKindCloud, "https://cluster.example", "ca", clusterbinding.ErrNameBlank},
		{"invalid auth kind", "prod", clusterbinding.AuthKind("manual"), "https://cluster.example", "ca", clusterbinding.ErrInvalidAuthKind},
		{"cloud missing server url", "prod", clusterbinding.AuthKindCloud, "", "ca", clusterbinding.ErrServerURLRequired},
		{"cloud missing ca bundle", "prod", clusterbinding.AuthKindCloud, "https://cluster.example", "", clusterbinding.ErrCABundleRequired},
		{"oidc missing server url", "prod", clusterbinding.AuthKindOIDC, "", "ca", clusterbinding.ErrServerURLRequired},
		{"oidc missing ca bundle", "prod", clusterbinding.AuthKindOIDC, "https://cluster.example", "", clusterbinding.ErrCABundleRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := clusterbinding.Validate(tc.bindName, tc.authKind, tc.serverURL, tc.caBundle)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("Validate(...) = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(...) = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateParams(t *testing.T) {
	tests := []struct {
		name     string
		authKind clusterbinding.AuthKind
		raw      string
		wantErr  error
	}{
		{"cloud aws ok", clusterbinding.AuthKindCloud, `{"cloud":"aws"}`, nil},
		{"cloud gcp ok", clusterbinding.AuthKindCloud, `{"cloud":"gcp"}`, nil},
		{"cloud azure ok", clusterbinding.AuthKindCloud, `{"cloud":"azure"}`, nil},
		{"cloud with region ok", clusterbinding.AuthKindCloud, `{"cloud":"aws","region":"us-east-1"}`, nil},
		{"cloud generic rejected -- not a real cloud/exec plugin", clusterbinding.AuthKindCloud, `{"cloud":"generic"}`, clusterbinding.ErrCloudRequired},
		{"cloud missing", clusterbinding.AuthKindCloud, `{}`, clusterbinding.ErrCloudRequired},
		{"cloud malformed json", clusterbinding.AuthKindCloud, `not json`, clusterbinding.ErrParamsMalformed},
		{"oidc clientId ok", clusterbinding.AuthKindOIDC, `{"clientId":"my-client"}`, nil},
		{"oidc missing clientId", clusterbinding.AuthKindOIDC, `{}`, clusterbinding.ErrClientIDRequired},
		{"oidc blank clientId", clusterbinding.AuthKindOIDC, `{"clientId":"  "}`, clusterbinding.ErrClientIDRequired},
		{"static secretName ok", clusterbinding.AuthKindStatic, `{"secretName":"KUBE_STATIC_CONFIG"}`, nil},
		{"static missing secretName", clusterbinding.AuthKindStatic, `{}`, clusterbinding.ErrSecretNameRequired},
		{"unrecognized auth kind", clusterbinding.AuthKind("manual"), `{}`, clusterbinding.ErrInvalidAuthKind},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := clusterbinding.ValidateParams(tc.authKind, json.RawMessage(tc.raw))
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateParams(...) = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateParams(...) = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}

// TestReservedEnvVarNames_ContainsKubeconfig pins the ONE reserved name
// this package's own kubeconfig mechanism owns -- the exact list
// internal/domain/sandboxsecret.ValidateName also reserves from (see that
// package's own name.go top doc comment).
func TestReservedEnvVarNames_ContainsKubeconfig(t *testing.T) {
	names := clusterbinding.ReservedEnvVarNames()
	if len(names) != 1 || names[0] != clusterbinding.EnvVarKubeconfig {
		t.Errorf("ReservedEnvVarNames() = %v, want [%q]", names, clusterbinding.EnvVarKubeconfig)
	}
	if clusterbinding.EnvVarKubeconfig != "KUBECONFIG" {
		t.Errorf("EnvVarKubeconfig = %q, want %q", clusterbinding.EnvVarKubeconfig, "KUBECONFIG")
	}
}

// TestReservedEnvVarNames_DefensiveCopy proves mutating the returned slice
// never corrupts a later call's own result -- mirrors
// providercredential.EnvVarNames' own identical defensive-copy guarantee.
func TestReservedEnvVarNames_DefensiveCopy(t *testing.T) {
	first := clusterbinding.ReservedEnvVarNames()
	first[0] = "CORRUPTED"
	second := clusterbinding.ReservedEnvVarNames()
	if second[0] != clusterbinding.EnvVarKubeconfig {
		t.Errorf("ReservedEnvVarNames() mutated by a prior caller's own slice: got %q, want %q", second[0], clusterbinding.EnvVarKubeconfig)
	}
}
