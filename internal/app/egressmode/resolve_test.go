// Unit tests for Resolve -- §30.8's own "dedicated test pinning the
// resolver to 'suppress' on pgx.ErrNoRows, on arbitrary error, and on an
// absent row" requirement, less the third case: an absent row against a
// REAL Postgres is resolve_integration_test.go's own job, deliberately
// kept out of this file (see that file's own doc comment, and
// deps.go's own RepoSettingsReader doc comment, for why a fake standing
// in for "the row is absent" would not actually test what §30.8 asks
// for).
package egressmode_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/egressmode"
)

// fakeRepoSettingsReader is a minimal double satisfying
// egressmode.RepoSettingsReader, returning one canned (settings, err)
// pair and recording how it was called.
type fakeRepoSettingsReader struct {
	settings sqlcgen.RepoSetting
	err      error

	calls    int
	lastRepo string
}

func (f *fakeRepoSettingsReader) Get(_ context.Context, repoFullName string) (sqlcgen.RepoSetting, error) {
	f.calls++
	f.lastRepo = repoFullName
	return f.settings, f.err
}

// TestResolve_ErrNoRows_ResolvesShadow is §30.8's own first pinned case:
// a missing repo_settings row (pgx.ErrNoRows) must resolve shadow -- the
// "every newly connected repo starts in shadow" onboarding property.
func TestResolve_ErrNoRows_ResolvesShadow(t *testing.T) {
	reader := &fakeRepoSettingsReader{err: pgx.ErrNoRows}

	got := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if got.Live() {
		t.Error("Resolve().Live() = true, want false (pgx.ErrNoRows must resolve shadow, §30.8)")
	}
	if !got.Suppressed() {
		t.Error("Resolve().Suppressed() = false, want true")
	}
}

// TestResolve_ArbitraryError_ResolvesShadow is §30.8's own second pinned
// case: a GENUINE read failure (anything other than pgx.ErrNoRows) --
// a connection error, a timeout, a context cancellation -- must resolve
// shadow, never fall back to some other default. This is the "suppress-
// on-error for free" property the polarity of live_egress_enabled exists
// to buy.
func TestResolve_ArbitraryError_ResolvesShadow(t *testing.T) {
	reader := &fakeRepoSettingsReader{err: errors.New("connection reset by peer")}

	got := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if got.Live() {
		t.Error("Resolve().Live() = true, want false (an arbitrary repo_settings read error must resolve shadow, §30.8)")
	}
}

// TestResolve_LiveEgressEnabledTrue_ResolvesLive is the one path that
// must NOT resolve shadow: an existing row that explicitly opted in,
// with no platform-level override in force.
func TestResolve_LiveEgressEnabledTrue_ResolvesLive(t *testing.T) {
	reader := &fakeRepoSettingsReader{settings: sqlcgen.RepoSetting{LiveEgressEnabled: true}}

	got := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if !got.Live() {
		t.Error("Resolve().Live() = false, want true when live_egress_enabled=true and no platform override is set")
	}
	if got.Suppressed() {
		t.Error("Resolve().Suppressed() = true, want false")
	}
}

// TestResolve_LiveEgressEnabledFalse_ResolvesShadow pins the explicit,
// non-error path distinctly from TestResolve_ErrNoRows_ResolvesShadow
// above -- an EXISTING row that has simply never been promoted must
// resolve exactly the same as a missing one.
func TestResolve_LiveEgressEnabledFalse_ResolvesShadow(t *testing.T) {
	reader := &fakeRepoSettingsReader{settings: sqlcgen.RepoSetting{LiveEgressEnabled: false}}

	got := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if got.Live() {
		t.Error("Resolve().Live() = true, want false when live_egress_enabled=false")
	}
}

// TestResolve_PlatformShadow_ForcesShadowWithoutConsultingRepoSettings
// covers §30.8's master switch: platformShadow=true must force shadow
// EVEN when the per-repo row says live -- "platformShadow OR NOT
// live_egress_enabled" -- and must do so without ever calling
// RepoSettings.Get at all (Resolve's own doc comment: a dedicated
// evaluation deployment must stay provably shadow even if repo_settings
// is unreachable).
func TestResolve_PlatformShadow_ForcesShadowWithoutConsultingRepoSettings(t *testing.T) {
	reader := &fakeRepoSettingsReader{settings: sqlcgen.RepoSetting{LiveEgressEnabled: true}}

	got := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader, PlatformShadow: true}, "acme/widget")

	if got.Live() {
		t.Error("Resolve().Live() = true, want false: NARVI_SHADOW_MODE must force shadow regardless of live_egress_enabled")
	}
	if reader.calls != 0 {
		t.Errorf("RepoSettings.Get called %d time(s), want 0: PlatformShadow must short-circuit before any Postgres read", reader.calls)
	}
}

// TestResolve_PassesRepoFullNameThrough guards against a trivial but real
// mistake -- swapping or hardcoding the repo argument -- that every other
// assertion here would miss, since every fixture above uses the same
// literal for both the call and its own assertions.
func TestResolve_PassesRepoFullNameThrough(t *testing.T) {
	reader := &fakeRepoSettingsReader{settings: sqlcgen.RepoSetting{LiveEgressEnabled: true}}

	egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if reader.lastRepo != "acme/widget" {
		t.Errorf("RepoSettings.Get called with repoFullName = %q, want %q", reader.lastRepo, "acme/widget")
	}
}

// TestCapability_ZeroValueIsShadow pins the "impossible to misuse"
// property capability.go's own doc comment claims: a Capability obtained
// any way OTHER than through Resolve (a forgotten assignment, a bare var
// declaration, a zero-initialized struct field) is always shadow.
func TestCapability_ZeroValueIsShadow(t *testing.T) {
	var zero egressmode.Capability

	if zero.Live() {
		t.Error("zero-value Capability.Live() = true, want false")
	}
	if !zero.Suppressed() {
		t.Error("zero-value Capability.Suppressed() = false, want true")
	}
	if got := zero.String(); got != "shadow" {
		t.Errorf("zero-value Capability.String() = %q, want %q", got, "shadow")
	}
}

// TestCapability_String_ReflectsLive is the live-side counterpart to
// TestCapability_ZeroValueIsShadow above.
func TestCapability_String_ReflectsLive(t *testing.T) {
	reader := &fakeRepoSettingsReader{settings: sqlcgen.RepoSetting{LiveEgressEnabled: true}}
	live := egressmode.Resolve(context.Background(), egressmode.Deps{RepoSettings: reader}, "acme/widget")

	if got := live.String(); got != "live" {
		t.Errorf("live Capability.String() = %q, want %q", got, "live")
	}
}
