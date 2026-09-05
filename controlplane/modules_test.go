package controlplane

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"testing"
	"time"

	"github.com/narvidev/narvi/extension"
	"github.com/narvidev/narvi/internal/domain/license"
	"github.com/narvidev/narvi/internal/platform"
)

// TestValidateModules covers every way a composed module's own declared
// shape can be inconsistent -- docs/design/boundaries-design.md, section
// 3.2's own Name and Capabilities doc comments -- and the clean,
// well-formed case.
func TestValidateModules(t *testing.T) {
	tests := []struct {
		name    string
		modules []extension.Module
		wantErr bool
	}{
		{name: "no modules", modules: nil, wantErr: false},
		{name: "one well-formed module, no capabilities", modules: []extension.Module{{Name: "acme"}}, wantErr: false},
		{
			name:    "one well-formed module, a known capability",
			modules: []extension.Module{{Name: "acme", Capabilities: []extension.Capability{extension.CapabilityGovernance}}},
			wantErr: false,
		},
		{
			name:    "two well-formed modules, distinct names",
			modules: []extension.Module{{Name: "acme"}, {Name: "widgets"}},
			wantErr: false,
		},
		{name: "empty name", modules: []extension.Module{{Name: ""}}, wantErr: true},
		{name: "malformed name: uppercase", modules: []extension.Module{{Name: "Acme"}}, wantErr: true},
		{name: "malformed name: underscore", modules: []extension.Module{{Name: "acme_corp"}}, wantErr: true},
		{name: "malformed name: space", modules: []extension.Module{{Name: "acme corp"}}, wantErr: true},
		{
			name:    "duplicate names",
			modules: []extension.Module{{Name: "acme"}, {Name: "acme"}},
			wantErr: true,
		},
		{
			name:    "unknown capability",
			modules: []extension.Module{{Name: "acme", Capabilities: []extension.Capability{"not-a-real-capability"}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModules(tt.modules)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateModules() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateModules_JoinsEveryProblem proves every defect is reported
// at once (errors.Join), mirroring platform.Timeouts.Validate's own
// identical "report every violation, not just one" precedent -- a boot
// refusal should never make an operator fix one problem only to
// discover a second on the next attempt.
func TestValidateModules_JoinsEveryProblem(t *testing.T) {
	err := validateModules([]extension.Module{
		{Name: "Not Valid"},
		{Name: "acme", Capabilities: []extension.Capability{"nonsense"}},
	})
	if err == nil {
		t.Fatal("validateModules() = nil, want an error naming both problems")
	}

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("validateModules() error = %v (%T), want an errors.Join tree", err, err)
	}
	if got := len(joined.Unwrap()); got != 2 {
		t.Errorf("validateModules() joined %d errors, want 2 (one per malformed module)", got)
	}
}

// TestUnionCapabilities proves the installed set is the deduplicated
// union of every composed module's own declared Capabilities.
func TestUnionCapabilities(t *testing.T) {
	modules := []extension.Module{
		{Name: "a", Capabilities: []extension.Capability{extension.CapabilityGovernance}},
		{Name: "b", Capabilities: []extension.Capability{extension.CapabilityGovernance, extension.CapabilityKnowledgeRetrieval}},
	}

	got := unionCapabilities(modules)
	want := map[license.Capability]bool{license.CapabilityGovernance: true, license.CapabilityKnowledgeRetrieval: true}

	if len(got) != len(want) {
		t.Fatalf("unionCapabilities() = %v, want exactly %d entries matching %v", got, len(want), want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unionCapabilities() contains unexpected %q", c)
		}
	}
}

// TestUnionCapabilities_NoModules proves the public binary's own shape:
// zero modules composed means an empty installed set.
func TestUnionCapabilities_NoModules(t *testing.T) {
	if got := unionCapabilities(nil); len(got) != 0 {
		t.Errorf("unionCapabilities(nil) = %v, want empty", got)
	}
}

// fakeFS is a minimal, comparable fs.FS -- a bare string underlying type,
// deliberately not fstest.MapFS (a map type, uncomparable with ==) --
// used only so TestSelectWebAssets can tell which value selectWebAssets
// returned by simple equality.
type fakeFS string

func (f fakeFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// TestSelectWebAssets covers no modules, a module that supplies no
// WebAssets, one that does, and confirms the later of two suppliers wins
// (docs/design/boundaries-design.md, section 3.2: "WebAssets, when
// non-nil, replaces webui.DistFS").
func TestSelectWebAssets(t *testing.T) {
	fallback := fakeFS("fallback")
	moduleAssets := fakeFS("module")

	tests := []struct {
		name    string
		modules []extension.Module
		want    fs.FS
	}{
		{name: "no modules", modules: nil, want: fallback},
		{name: "module without web assets", modules: []extension.Module{{Name: "a"}}, want: fallback},
		{name: "module with web assets", modules: []extension.Module{{Name: "a", WebAssets: moduleAssets}}, want: moduleAssets},
		{
			name: "last supplier wins",
			modules: []extension.Module{
				{Name: "a", WebAssets: fakeFS("first")},
				{Name: "b", WebAssets: moduleAssets},
			},
			want: moduleAssets,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectWebAssets(fallback, tt.modules); got != tt.want {
				t.Errorf("selectWebAssets() = %v, want %v", got, tt.want)
			}
		})
	}
}

// countingCapabilities is a counting fake for extension.Capabilities --
// the same one-method interface a composed module itself receives --
// used to prove logLicenseBoot never calls Enabled when modules is
// empty.
type countingCapabilities struct{ calls int }

func (c *countingCapabilities) Enabled(extension.Capability) bool {
	c.calls++
	return false
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBuild_WithoutModules_NeverConsultsCapabilities is docs/design/
// boundaries-design.md, section 1.6's own named test, proving technical
// plan §34.5's own guarantee: with zero modules composed, nothing Build
// does ever consults the capability registry.
//
// Driven directly against logLicenseBoot -- the exact function
// buildCapabilityRegistry (and therefore Build) always calls to decide
// this boot's own design-note section-1.3 log lines -- rather than the
// full Build, because Build itself needs a real, migrated Postgres pool
// to get anywhere near this code path, and this particular guarantee
// needs none: it holds regardless of what a real registry would have
// said.
//
// grant here is deliberately a FULLY VALID, FULLY GRANTED licence -- the
// one shape that WOULD produce an Enabled/log call if this guard were
// ever bypassed -- so a stub that always answers false cannot make this
// test pass by accident.
func TestBuild_WithoutModules_NeverConsultsCapabilities(t *testing.T) {
	stub := &countingCapabilities{}
	now := time.Now()
	grant := &license.Grant{
		Capabilities: []license.Capability{license.CapabilityGovernance, license.CapabilityKnowledgeRetrieval},
		NotBefore:    now.Add(-time.Hour),
		ExpiresAt:    now.Add(time.Hour),
	}

	logLicenseBoot(discardLogger(), nil, stub, grant, nil, now, 0)

	if stub.calls != 0 {
		t.Errorf("logLicenseBoot() with zero modules called Capabilities.Enabled() %d times, want 0", stub.calls)
	}
}

// TestLogLicenseBoot_WithModules_ConsultsCapabilities is
// TestBuild_WithoutModules_NeverConsultsCapabilities's own mutation
// check, made explicit as a test: proves the zero-modules guard actually
// discriminates on modules, rather than logLicenseBoot simply never
// calling Enabled at all (which would make the sibling test above pass
// for the wrong reason).
func TestLogLicenseBoot_WithModules_ConsultsCapabilities(t *testing.T) {
	stub := &countingCapabilities{}
	now := time.Now()
	grant := &license.Grant{
		Capabilities: []license.Capability{license.CapabilityGovernance},
		NotBefore:    now.Add(-time.Hour),
		ExpiresAt:    now.Add(time.Hour),
	}

	logLicenseBoot(discardLogger(), []extension.Module{{Name: "acme"}}, stub, grant, nil, now, 0)

	if stub.calls == 0 {
		t.Error("logLicenseBoot() with a composed module called Capabilities.Enabled() 0 times, want at least 1 -- otherwise the zero-modules test proves nothing")
	}
}

// TestBuild_RefusesInconsistentModule proves Build itself -- not merely
// validateModules in isolation -- refuses to boot on a malformed module
// Name. Passes a nil pool: validateModules runs before Build ever
// touches cfg or pool, so this is safe and needs no real Postgres.
func TestBuild_RefusesInconsistentModule(t *testing.T) {
	_, err := Build(context.Background(), &platform.Config{}, nil, extension.Module{Name: "Not Valid!"})
	if err == nil {
		t.Fatal("Build() error = nil, want a boot refusal for a malformed module Name")
	}
	var invalid *InvalidModuleError
	if !errors.As(err, &invalid) {
		t.Errorf("Build() error = %v, want it to wrap an *InvalidModuleError", err)
	}
}

// TestBuild_RefusesUnimplementedCapability is TestBuild_RefusesInconsistentModule's
// own sibling for the other half of the same refusal: a well-formed Name
// declaring a capability this build does not define.
func TestBuild_RefusesUnimplementedCapability(t *testing.T) {
	_, err := Build(context.Background(), &platform.Config{}, nil, extension.Module{
		Name:         "acme",
		Capabilities: []extension.Capability{"not-a-real-capability"},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want a boot refusal for a capability this build does not implement")
	}
	var invalid *InvalidModuleError
	if !errors.As(err, &invalid) {
		t.Errorf("Build() error = %v, want it to wrap an *InvalidModuleError", err)
	}
}
