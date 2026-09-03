package cloudidentity_test

import (
	"errors"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/cloudidentity"
	"github.com/narvidev/narvi/internal/domain/providercredential"
)

func TestIsValidKind(t *testing.T) {
	tests := []struct {
		name string
		k    cloudidentity.Kind
		want bool
	}{
		{"aws", cloudidentity.KindAWS, true},
		{"gcp", cloudidentity.KindGCP, true},
		{"azure", cloudidentity.KindAzure, true},
		{"generic", cloudidentity.KindGeneric, true},
		{"empty", cloudidentity.Kind(""), false},
		{"unrecognized", cloudidentity.Kind("digitalocean"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudidentity.IsValidKind(tc.k); got != tc.want {
				t.Errorf("IsValidKind(%q) = %v, want %v", tc.k, got, tc.want)
			}
		})
	}
}

func TestAllKinds_EveryEntryIsValid(t *testing.T) {
	if len(cloudidentity.AllKinds) != 4 {
		t.Fatalf("len(AllKinds) = %d, want 4", len(cloudidentity.AllKinds))
	}
	for _, k := range cloudidentity.AllKinds {
		if !cloudidentity.IsValidKind(k) {
			t.Errorf("AllKinds contains %q, which IsValidKind rejects", k)
		}
	}
}

func TestIsValidBindingScope(t *testing.T) {
	tests := []struct {
		name string
		s    providercredential.Scope
		want bool
	}{
		{"environment", providercredential.ScopeEnvironment, true},
		{"global", providercredential.ScopeGlobal, true},
		{"repo is NOT a valid binding scope (§27.3: deliberately no repo scope)", providercredential.ScopeRepo, false},
		{"user is not a valid binding scope", providercredential.ScopeUser, false},
		{"automation is not a valid binding scope", providercredential.ScopeAutomation, false},
		{"empty", providercredential.Scope(""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudidentity.IsValidBindingScope(tc.s); got != tc.want {
				t.Errorf("IsValidBindingScope(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestSub(t *testing.T) {
	tests := []struct {
		name          string
		environmentID string
		want          string
	}{
		{"typical uuid", "3fa85f64-5717-4562-b3fc-2c963f66afa6", "narvi:environment:3fa85f64-5717-4562-b3fc-2c963f66afa6"},
		{"empty id still produces the fixed prefix", "", "narvi:environment:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudidentity.Sub(tc.environmentID); got != tc.want {
				t.Errorf("Sub(%q) = %q, want %q", tc.environmentID, got, tc.want)
			}
		})
	}
}

// TestValidateBinding_AzureGlobalForbidden mutation-tests this Step's own
// gap-3 resolution: removing the kind==azure && scope==global branch
// inside ValidateBinding must make this test fail. See this package's own
// doc.go for the full "why" (Azure's exact-match-only federated-credential
// matching cannot honestly express "trust every Environment" from a
// single global-scope binding).
func TestValidateBinding_AzureGlobalForbidden(t *testing.T) {
	err := cloudidentity.ValidateBinding(cloudidentity.KindAzure, providercredential.ScopeGlobal)
	if !errors.Is(err, cloudidentity.ErrAzureGlobalScopeForbidden) {
		t.Fatalf("ValidateBinding(azure, global) = %v, want ErrAzureGlobalScopeForbidden", err)
	}
}

func TestValidateBinding(t *testing.T) {
	tests := []struct {
		name    string
		kind    cloudidentity.Kind
		scope   providercredential.Scope
		wantErr error // nil means "no error"
	}{
		{"aws + environment ok", cloudidentity.KindAWS, providercredential.ScopeEnvironment, nil},
		{"aws + global ok", cloudidentity.KindAWS, providercredential.ScopeGlobal, nil},
		{"gcp + environment ok", cloudidentity.KindGCP, providercredential.ScopeEnvironment, nil},
		{"gcp + global ok", cloudidentity.KindGCP, providercredential.ScopeGlobal, nil},
		{"generic + environment ok", cloudidentity.KindGeneric, providercredential.ScopeEnvironment, nil},
		{"generic + global ok", cloudidentity.KindGeneric, providercredential.ScopeGlobal, nil},
		{"azure + environment ok", cloudidentity.KindAzure, providercredential.ScopeEnvironment, nil},
		{"azure + global forbidden", cloudidentity.KindAzure, providercredential.ScopeGlobal, cloudidentity.ErrAzureGlobalScopeForbidden},
		{"invalid kind", cloudidentity.Kind("bogus"), providercredential.ScopeEnvironment, cloudidentity.ErrInvalidKind},
		{"invalid scope (repo)", cloudidentity.KindAWS, providercredential.ScopeRepo, cloudidentity.ErrInvalidScope},
		{"invalid scope (user)", cloudidentity.KindAWS, providercredential.ScopeUser, cloudidentity.ErrInvalidScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cloudidentity.ValidateBinding(tc.kind, tc.scope)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateBinding(%q, %q) = %v, want nil", tc.kind, tc.scope, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateBinding(%q, %q) = %v, want %v", tc.kind, tc.scope, err, tc.wantErr)
			}
		})
	}
}

func TestValidateAudience(t *testing.T) {
	tests := []struct {
		name     string
		audience string
		wantErr  error
	}{
		{"typical aws value", "sts.amazonaws.com", nil},
		{"empty", "", cloudidentity.ErrBlankAudience},
		{"whitespace only", "   ", cloudidentity.ErrBlankAudience},
		{"embedded NUL", "sts\x00.amazonaws.com", cloudidentity.ErrAudienceContainsNUL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cloudidentity.ValidateAudience(tc.audience)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateAudience(%q) = %v, want nil", tc.audience, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateAudience(%q) = %v, want %v", tc.audience, err, tc.wantErr)
			}
		})
	}
}

func TestBuildClaims(t *testing.T) {
	issuedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lifetime := 10 * time.Minute
	tag := "prototyping"

	got := cloudidentity.BuildClaims(cloudidentity.BuildClaimsInput{
		Issuer:        "https://issuer.narvi.example.test",
		EnvironmentID: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Audience:      "sts.amazonaws.com",
		IssuedAt:      issuedAt,
		Lifetime:      lifetime,
		SessionID:     "session-1",
		Gen:           3,
		Repos:         []string{"acme/widgets"},
		ProvenanceTag: &tag,
	})

	if got.Issuer != "https://issuer.narvi.example.test" {
		t.Errorf("Issuer = %q", got.Issuer)
	}
	wantSub := "narvi:environment:3fa85f64-5717-4562-b3fc-2c963f66afa6"
	if got.Subject != wantSub {
		t.Errorf("Subject = %q, want %q", got.Subject, wantSub)
	}
	if got.Audience != "sts.amazonaws.com" {
		t.Errorf("Audience = %q", got.Audience)
	}
	if got.IssuedAt != issuedAt.Unix() {
		t.Errorf("IssuedAt = %d, want %d", got.IssuedAt, issuedAt.Unix())
	}
	wantExp := issuedAt.Add(lifetime).Unix()
	if got.Expiry != wantExp {
		t.Errorf("Expiry = %d, want %d", got.Expiry, wantExp)
	}
	if got.SessionID != "session-1" || got.Gen != 3 {
		t.Errorf("SessionID/Gen = %q/%d, want session-1/3", got.SessionID, got.Gen)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "acme/widgets" {
		t.Errorf("Repos = %v, want [acme/widgets]", got.Repos)
	}
	if got.ProvenanceTag == nil || *got.ProvenanceTag != "prototyping" {
		t.Errorf("ProvenanceTag = %v, want prototyping", got.ProvenanceTag)
	}
}

// TestBuildClaims_SubjectNeverCarriesSessionContext pins §27.3's own
// "never anything session-varying in sub" invariant: two calls that
// differ ONLY in session-varying fields (SessionID/Gen/Repos/
// ProvenanceTag), holding EnvironmentID fixed, must produce the identical
// Subject. A regression that accidentally folds any session-varying value
// into Subject fails this test.
func TestBuildClaims_SubjectNeverCarriesSessionContext(t *testing.T) {
	base := cloudidentity.BuildClaimsInput{
		Issuer:        "https://issuer.narvi.example.test",
		EnvironmentID: "env-fixed",
		Audience:      "sts.amazonaws.com",
		IssuedAt:      time.Now(),
		Lifetime:      time.Minute,
		SessionID:     "session-a",
		Gen:           1,
	}
	variant := base
	variant.SessionID = "session-b-completely-different"
	variant.Gen = 99
	variant.Repos = []string{"a/b", "c/d"}
	tag := "different-tag"
	variant.ProvenanceTag = &tag

	got1 := cloudidentity.BuildClaims(base)
	got2 := cloudidentity.BuildClaims(variant)

	if got1.Subject != got2.Subject {
		t.Fatalf("Subject differs across session-varying-only changes: %q vs %q", got1.Subject, got2.Subject)
	}
	if got1.Subject != "narvi:environment:env-fixed" {
		t.Fatalf("Subject = %q, want narvi:environment:env-fixed", got1.Subject)
	}
}
