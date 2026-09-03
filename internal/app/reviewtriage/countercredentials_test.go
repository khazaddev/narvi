package reviewtriage_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/reviewtriage"
)

// TestCredentialedProviders_NoRows proves the overwhelming common case
// (ListForResolution returned nothing -- no provider_credentials row
// applies to this session at all) reduces to an empty, non-nil set, never
// a panic on a later map read (a nil map read is also safe in Go, but the
// function's own doc comment promises "the set", not "possibly nil" --
// pinned here so that promise stays true).
func TestCredentialedProviders_NoRows(t *testing.T) {
	got := reviewtriage.CredentialedProviders(nil)
	if len(got) != 0 {
		t.Errorf("CredentialedProviders(nil) = %v, want empty", got)
	}
}

// TestCredentialedProviders_OneRecognizedScopeRow proves a single
// recognized-scope row (global, the always-available fallback scope) makes
// its own provider resolve as credentialed, and leaves every OTHER
// provider absent from the set entirely (never a false explicit entry).
func TestCredentialedProviders_OneRecognizedScopeRow(t *testing.T) {
	rows := []sqlcgen.ProviderCredential{
		{Provider: sqlcgen.ProviderCredentialProviderAnthropic, Scope: sqlcgen.ProviderCredentialScopeGlobal},
	}
	got := reviewtriage.CredentialedProviders(rows)
	if !got["anthropic"] {
		t.Errorf("CredentialedProviders(%v)[\"anthropic\"] = false, want true", rows)
	}
	if got["openai"] {
		t.Errorf("CredentialedProviders(%v)[\"openai\"] = true, want false (absent -- no row at all)", rows)
	}
	if got["google"] {
		t.Errorf("CredentialedProviders(%v)[\"google\"] = true, want false (absent -- no row at all)", rows)
	}
}

// TestCredentialedProviders_MultipleProviders proves every provider with
// at least one recognized-scope row resolves independently -- this is a
// SET reduction (existence per provider), not a single winner across
// providers the way providercredential.Resolve itself picks one winning
// row within a single provider's own candidates.
func TestCredentialedProviders_MultipleProviders(t *testing.T) {
	rows := []sqlcgen.ProviderCredential{
		{Provider: sqlcgen.ProviderCredentialProviderAnthropic, Scope: sqlcgen.ProviderCredentialScopeGlobal},
		{Provider: sqlcgen.ProviderCredentialProviderOpenai, Scope: sqlcgen.ProviderCredentialScopeRepo, ScopeTargetID: strPtrForTest("acme/widgets")},
	}
	got := reviewtriage.CredentialedProviders(rows)
	if !got["anthropic"] || !got["openai"] {
		t.Errorf("CredentialedProviders(%v) = %v, want both anthropic and openai credentialed", rows, got)
	}
	if got["google"] {
		t.Errorf("CredentialedProviders(%v)[\"google\"] = true, want false (no row at all)", rows)
	}
}

// TestCredentialedProviders_UnrecognizedScopeIgnored proves a row at a
// Scope providercredential.Resolve does not recognize (this test uses a
// deliberately bogus value -- IsValidScope's own closed vocabulary is
// user/environment/repo/global) never counts toward "credentialed",
// mirroring Resolve's own "an unrecognized Scope is silently ignored"
// doc comment (resolve.go) exactly -- this function must never diverge
// from that contract by treating row PRESENCE alone as sufficient.
func TestCredentialedProviders_UnrecognizedScopeIgnored(t *testing.T) {
	rows := []sqlcgen.ProviderCredential{
		{Provider: sqlcgen.ProviderCredentialProviderAnthropic, Scope: sqlcgen.ProviderCredentialScope("bogus-scope")},
	}
	got := reviewtriage.CredentialedProviders(rows)
	if got["anthropic"] {
		t.Errorf("CredentialedProviders(%v)[\"anthropic\"] = true, want false (unrecognized scope, mirrors Resolve's own contract)", rows)
	}
}

func strPtrForTest(s string) *string { return &s }
