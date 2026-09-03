package rwx

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/narvidev/narvi/contracts/gen/go/sessionconfig"
	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// White-box (`package rwx`, not rwx_test) so these tests can reach
// unexported members directly (newWithRunner, sandboxIdentityPath,
// classifyCLIError, ...) — the same convention
// internal/adapters/outbound/modal's own test files already use.

// testSessionConfig builds a realistic SESSION_CONFIG document (matching
// the shape modal/provider_test.go's own identical helper builds) for use
// as CreateSpec.SessionConfig across this file's tests.
func testSessionConfig(sessionID string, gen int) sessionconfig.SessionConfig {
	branch := "main"
	return sessionconfig.SessionConfig{
		SessionId:         sessionID,
		Gen:               gen,
		SandboxId:         "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a",
		SandboxToken:      "sandbox-token-plaintext",
		BootMode:          sessionconfig.SessionConfigBootModeFresh,
		ControlPlaneWsUrl: "wss://cp.narvi.dev/sessions/" + sessionID + "/ws?type=sandbox",
		Repos: []sessionconfig.SessionConfigReposElem{
			{Name: "narvi", Url: "https://github.com/narvidev/narvi.git", Branch: &branch},
		},
	}
}

func testConfig() Config {
	return Config{
		CLIPath:     "rwx",
		AccessToken: "test-access-token-super-secret",
		Timeouts:    platform.DefaultTimeouts(),
	}
}

// fakeCall records one fakeCLIRunner.Run invocation.
type fakeCall struct {
	args []string
	env  []string
	ctx  context.Context
}

// fakeCLIRunner is a test-only cliRunner recording every call it receives
// and returning a caller-configured (stdout, stderr, exitCode, err)
// tuple — the seam every test in this file uses instead of ever invoking
// a real `rwx` binary (see runner.go's own doc comment).
type fakeCLIRunner struct {
	mu    sync.Mutex
	calls []fakeCall

	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

func (f *fakeCLIRunner) Run(ctx context.Context, args []string, env []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{args: args, env: env, ctx: ctx})
	return f.stdout, f.stderr, f.exitCode, f.err
}

func (f *fakeCLIRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeCLIRunner) lastCall() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// --- New() / newWithRunner() validation ---

func TestNew_Validation(t *testing.T) {
	t.Run("missing CLI path", func(t *testing.T) {
		cfg := testConfig()
		cfg.CLIPath = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *MissingConfigError", err)
		}
	})

	t.Run("missing access token", func(t *testing.T) {
		cfg := testConfig()
		cfg.AccessToken = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *MissingConfigError", err)
		}
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		p, err := New(testConfig())
		if err != nil {
			t.Fatalf("New() error = %v, want nil", err)
		}
		if p == nil {
			t.Fatal("New() returned nil Provider with nil error")
		}
		if _, ok := p.runner.(execCLIRunner); !ok {
			t.Errorf("New() runner = %T, want execCLIRunner (the real transport)", p.runner)
		}
	})
}

// --- Capabilities ---

func TestProvider_Capabilities(t *testing.T) {
	runner := &fakeCLIRunner{}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	got := p.Capabilities()
	want := ports.Capabilities{Snapshots: false, Resume: false, ExplicitStop: true, ImageBuilds: false}
	if got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
	if runner.callCount() != 0 {
		t.Error("Capabilities() must never shell out to the CLI")
	}
}

// --- Unsupported operations: permanent, no subprocess call ---

func TestProvider_UnsupportedOperations_NeverShellOut(t *testing.T) {
	tests := []struct {
		name string
		op   ports.Op
		call func(p *Provider) error
	}{
		{
			name: "ResumeSandbox",
			op:   ports.OpResumeSandbox,
			call: func(p *Provider) error {
				return p.ResumeSandbox(context.Background(), ports.SandboxRef{ProviderID: "x"})
			},
		},
		{
			name: "TakeSnapshot",
			op:   ports.OpTakeSnapshot,
			call: func(p *Provider) error {
				_, err := p.TakeSnapshot(context.Background(), ports.SandboxRef{ProviderID: "x"})
				return err
			},
		},
		{
			name: "RestoreFromSnapshot",
			op:   ports.OpRestoreFromSnapshot,
			call: func(p *Provider) error {
				_, err := p.RestoreFromSnapshot(context.Background(), ports.SnapshotID("snap-1"), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)})
				return err
			},
		},
		{
			name: "BuildImage",
			op:   ports.OpBuildImage,
			call: func(p *Provider) error {
				_, err := p.BuildImage(context.Background(), ports.ImageSpec{Base: "base:v1"})
				return err
			},
		},
		{
			name: "DeleteImage",
			op:   ports.OpDeleteImage,
			call: func(p *Provider) error { return p.DeleteImage(context.Background(), ports.ImageRef("img-1")) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCLIRunner{}
			p, err := newWithRunner(testConfig(), runner)
			if err != nil {
				t.Fatalf("newWithRunner() error = %v", err)
			}

			err = tt.call(p)
			if err == nil {
				t.Fatal("call error = nil, want a permanent ProviderError")
			}
			if runner.callCount() != 0 {
				t.Errorf("%s made a subprocess call, want none (unsupported operation)", tt.name)
			}

			var pe *ports.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %v, want *ports.ProviderError", err)
			}
			if pe.Transient {
				t.Error("error.Transient = true, want false (permanent)")
			}
			if pe.Op != tt.op {
				t.Errorf("error.Op = %q, want %q", pe.Op, tt.op)
			}
			if pe.Code != "UNSUPPORTED_OPERATION" {
				t.Errorf("error.Code = %q, want %q", pe.Code, "UNSUPPORTED_OPERATION")
			}
			if ports.IsTransient(err) {
				t.Error("ports.IsTransient(err) = true, want false for a classified permanent error")
			}
		})
	}
}

// --- CreateSandbox: args/env construction, identity, RWX_ACCESS_TOKEN never in argv ---

func TestProvider_CreateSandbox_BuildsArgsAndEnv(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0}
	cfg := testConfig()
	p, err := newWithRunner(cfg, runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	spec := ports.CreateSpec{
		Gen:           2,
		Image:         "ghcr.io/acme/base:latest",
		SessionConfig: testSessionConfig("sess-abc", 2),
	}

	ref, err := p.CreateSandbox(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	wantIdentity := sandboxIdentityPath("sess-abc", 2)
	if ref.ProviderID != wantIdentity {
		t.Errorf("CreateSandbox() ref.ProviderID = %q, want %q", ref.ProviderID, wantIdentity)
	}

	if runner.callCount() != 1 {
		t.Fatalf("subprocess call count = %d, want 1", runner.callCount())
	}
	call := runner.lastCall()

	wantArgs := []string{
		"sandbox", "start",
		"--format", "json",
		"--config", wantIdentity,
		"--inactivity-timeout", cfg.Timeouts.RWXSandboxInactivityTimeout.String(),
		"--base", spec.Image,
	}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}

	if !containsEnvEntry(call.env, rwxAccessTokenEnvVar, cfg.AccessToken) {
		t.Errorf("env = %v, want it to contain %s=%s", call.env, rwxAccessTokenEnvVar, cfg.AccessToken)
	}

	// SESSION_CONFIG must travel as ONE opaque JSON value (§4.1: "the
	// provider never assembles env fragments") -- decode it back and
	// compare against the original struct, proving nothing was spread
	// across separate env entries or CLI flags.
	rawSessionConfig, ok := envValue(call.env, sessionConfigEnvVar)
	if !ok {
		t.Fatalf("env missing %s entry", sessionConfigEnvVar)
	}
	var decoded sessionconfig.SessionConfig
	if err := json.Unmarshal([]byte(rawSessionConfig), &decoded); err != nil {
		t.Fatalf("decode %s: %v", sessionConfigEnvVar, err)
	}
	if !reflect.DeepEqual(decoded, spec.SessionConfig) {
		t.Errorf("decoded %s = %+v, want %+v", sessionConfigEnvVar, decoded, spec.SessionConfig)
	}

	// No CLI flag/arg may ever carry the access token or the plaintext
	// sandbox bearer token embedded in SessionConfig (§5.2's leak-class
	// discipline: argv is visible to process listings).
	for _, arg := range call.args {
		if strings.Contains(arg, cfg.AccessToken) {
			t.Errorf("argv contains the access token: %q", arg)
		}
		if strings.Contains(arg, spec.SessionConfig.SandboxToken) {
			t.Errorf("argv contains the sandbox bearer token: %q", arg)
		}
	}
}

// TestProvider_Env_AllowlistsAmbientEnvironment is FIX-5/S1's own
// regression test for the env-allowlist security fix (env's own doc
// comment, provider.go): overrides osEnviron (runner.go's own package var,
// which exists "purely so tests can override it" -- until now, no test
// ever did) to inject BOTH an allowlisted sentinel (HTTPS_PROXY) and a
// non-allowlisted one shaped exactly like a real control-plane secret
// (NARVI_TOKEN_ENCRYPTION_KEY) into the ambient environment, then proves
// via runner.lastCall().env that the allowlisted entry survives into the
// `rwx` CLI subprocess's own environment, the secret-shaped one is
// completely absent, and RWX_ACCESS_TOKEN/SESSION_CONFIG (this Provider's
// own two deliberate additions) are still both present.
func TestProvider_Env_AllowlistsAmbientEnvironment(t *testing.T) {
	origEnviron := osEnviron
	osEnviron = func() []string {
		return []string{
			"PATH=/usr/bin:/bin",
			"HTTPS_PROXY=http://proxy.test:3128",
			"NARVI_TOKEN_ENCRYPTION_KEY=super-secret",
		}
	}
	t.Cleanup(func() { osEnviron = origEnviron })

	runner := &fakeCLIRunner{exitCode: 0}
	cfg := testConfig()
	p, err := newWithRunner(cfg, runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)}
	if _, err := p.CreateSandbox(context.Background(), spec); err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	env := runner.lastCall().env
	if !containsEnvEntry(env, "HTTPS_PROXY", "http://proxy.test:3128") {
		t.Errorf("env = %v, want it to contain the allowlisted HTTPS_PROXY entry", env)
	}
	if _, ok := envValue(env, "NARVI_TOKEN_ENCRYPTION_KEY"); ok {
		t.Errorf("env = %v, must NEVER contain a non-allowlisted control-plane secret", env)
	}
	if !containsEnvEntry(env, rwxAccessTokenEnvVar, cfg.AccessToken) {
		t.Errorf("env = %v, want it to contain %s=%s", env, rwxAccessTokenEnvVar, cfg.AccessToken)
	}
	if _, ok := envValue(env, sessionConfigEnvVar); !ok {
		t.Errorf("env = %v, want it to contain a %s entry", env, sessionConfigEnvVar)
	}
}

func TestProvider_CreateSandbox_NoImage_OmitsBaseFlag(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)}
	if _, err := p.CreateSandbox(context.Background(), spec); err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	for _, arg := range runner.lastCall().args {
		if arg == "--base" {
			t.Error("args contain --base with an empty spec.Image, want it omitted entirely")
		}
	}
}

// TestProvider_CreateSandbox_IdentityEmbedsSessionAndGen proves two
// different gens of the SAME session never collide onto one RWX sandbox
// identity (§3.2 fencing at the provider's own identity layer, §4.1.1).
func TestProvider_CreateSandbox_IdentityEmbedsSessionAndGen(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	ref1, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-x", 1)})
	if err != nil {
		t.Fatalf("CreateSandbox() gen 1 error = %v", err)
	}
	ref2, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 2, SessionConfig: testSessionConfig("sess-x", 2)})
	if err != nil {
		t.Fatalf("CreateSandbox() gen 2 error = %v", err)
	}
	ref3, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-y", 1)})
	if err != nil {
		t.Fatalf("CreateSandbox() different session, gen 1 error = %v", err)
	}

	if ref1.ProviderID == ref2.ProviderID {
		t.Errorf("same session, different gens produced the SAME identity %q -- gens must never collide", ref1.ProviderID)
	}
	if ref1.ProviderID == ref3.ProviderID {
		t.Errorf("different sessions produced the SAME identity %q", ref1.ProviderID)
	}
	if !strings.Contains(ref1.ProviderID, "sess-x") || !strings.Contains(ref1.ProviderID, "1") {
		t.Errorf("identity %q does not embed both session id and gen", ref1.ProviderID)
	}
}

// TestProvider_CreateSandbox_RejectsGenMismatch proves CreateSandbox
// validates spec (ports.CreateSpec.Validate) before ever shelling out.
func TestProvider_CreateSandbox_RejectsGenMismatch(t *testing.T) {
	runner := &fakeCLIRunner{}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 2, SessionConfig: testSessionConfig("sess-1", 1)}
	_, err = p.CreateSandbox(context.Background(), spec)
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a ProviderError for a Gen/SessionConfig.Gen mismatch")
	}
	if runner.callCount() != 0 {
		t.Error("CreateSandbox() shelled out, want none for a spec that fails Validate")
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Transient {
		t.Error("CreateSandbox() error.Transient = true, want false (permanent)")
	}
	var genErr *ports.GenMismatchError
	if !errors.As(err, &genErr) {
		t.Fatalf("CreateSandbox() error = %v, want it to wrap *ports.GenMismatchError", err)
	}
}

// TestProvider_CreateSandbox_BoundsSubprocessWithContextDeadline proves
// every CLI call is bounded by platform.Timeouts.RWXCLIExecTimeout, never
// left to run against an unbounded context.
//
// Strengthened (audit fix, test-quality): the original version of this
// test only asserted a deadline exists at all, which would still pass even
// if CreateSandbox were accidentally bound by a completely unrelated
// timeout. This now pins time.Until(deadline) into
// (RWXCLIExecTimeout-30s, RWXCLIExecTimeout] -- a generous lower bound (30s
// is comfortably more than this test's own real, local-process execution
// latency between context.WithTimeout's own call and this assertion
// reading it back), never an exact-equality check, which would be flaky
// against real wall-clock elapsed time. Pinned against
// platform.DefaultTimeouts() directly (2m), rather than merely "some
// deadline", specifically to catch a future regression that accidentally
// swaps in RWXSandboxInactivityTimeout (45m, the CLI's own
// `--inactivity-timeout` flag VALUE -- wholly unrelated to how long the
// subprocess invocation itself is allowed to run) -- the two are
// confusably named but differ by more than 20x, so this bound is
// non-flaky against that specific mix-up.
func TestProvider_CreateSandbox_BoundsSubprocessWithContextDeadline(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	if _, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)}); err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	deadline, ok := runner.lastCall().ctx.Deadline()
	if !ok {
		t.Fatal("subprocess context has no deadline, want one bounded by RWXCLIExecTimeout")
	}
	if deadline.IsZero() {
		t.Error("subprocess context deadline is zero")
	}

	wantTimeout := platform.DefaultTimeouts().RWXCLIExecTimeout
	lowerBound := wantTimeout - 30*time.Second
	if remaining := time.Until(deadline); remaining <= lowerBound || remaining > wantTimeout {
		t.Errorf("time.Until(deadline) = %v, want within (%v, %v] (RWXCLIExecTimeout, not the confusable RWXSandboxInactivityTimeout)",
			remaining, lowerBound, wantTimeout)
	}
}

// TestProvider_CreateSandbox_Error is FIX-3's own regression test: every
// existing CreateSandbox test until now only ever drove fakeCLIRunner with
// exitCode: 0 (the happy path) -- mirrors TestProvider_StopSandbox_Error's
// own identical shape for the sibling method, proving a nonzero exit
// propagates as a typed *ports.ProviderError and the returned SandboxRef is
// the zero value, never a partially-populated one a caller might mistake
// for success.
func TestProvider_CreateSandbox_Error(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 1, stdout: []byte(`{"status":"error","error":"quota exceeded"}`)}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	ref, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)})
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a ProviderError for a nonzero exit")
	}
	if ref != (ports.SandboxRef{}) {
		t.Errorf("CreateSandbox() ref = %+v, want the zero value on error", ref)
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Op != ports.OpCreateSandbox {
		t.Errorf("error.Op = %q, want %q", pe.Op, ports.OpCreateSandbox)
	}
}

// TestProvider_CreateSandbox_ProcessNeverCompleted is FIX-3's own second
// regression test: exitCode -1 (runner.go's own sentinel for "the process
// never completed at all" -- a spawn failure, or killed by this call's own
// context deadline) must propagate as a transient PROCESS_ERROR whose
// error chain still carries the runner's own underlying cause. Asserted
// via errors.Is (not just a Code/Transient check) so a future refactor
// that accidentally drops the %w-wrapped underlying error out of
// classifyCLIError's PROCESS_ERROR branch fails this test, even though
// every other field would still look correct.
func TestProvider_CreateSandbox_ProcessNeverCompleted(t *testing.T) {
	underlying := errors.New("exec: \"rwx\": executable file not found in $PATH")
	runner := &fakeCLIRunner{exitCode: -1, err: underlying}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	ref, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig("sess-1", 1)})
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a ProviderError for a process that never completed")
	}
	if ref != (ports.SandboxRef{}) {
		t.Errorf("CreateSandbox() ref = %+v, want the zero value on error", ref)
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if !pe.Transient {
		t.Error("error.Transient = false, want true (a process-level failure is always transient)")
	}
	if pe.Code != "PROCESS_ERROR" {
		t.Errorf("error.Code = %q, want %q", pe.Code, "PROCESS_ERROR")
	}
	if !errors.Is(err, underlying) {
		t.Errorf("error chain does not wrap the runner's own underlying cause (errors.Is failed); CreateSandbox() error = %v", err)
	}
}

// --- StopSandbox ---

func TestProvider_StopSandbox_BuildsArgs(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	if err := p.StopSandbox(context.Background(), ports.SandboxRef{ProviderID: "narvi/sess-1/gen-1/sandbox.json"}); err != nil {
		t.Fatalf("StopSandbox() error = %v", err)
	}

	want := []string{"sandbox", "stop", "--format", "json", "--config", "narvi/sess-1/gen-1/sandbox.json"}
	if got := runner.lastCall().args; !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestProvider_StopSandbox_Error(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 1, stdout: []byte(`{"status":"error","error":"not found"}`)}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	err = p.StopSandbox(context.Background(), ports.SandboxRef{ProviderID: "missing"})
	if err == nil {
		t.Fatal("StopSandbox() error = nil, want a ProviderError for a nonzero exit")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("StopSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Op != ports.OpStopSandbox {
		t.Errorf("error.Op = %q, want %q", pe.Op, ports.OpStopSandbox)
	}
}

// --- List ---

func TestProvider_List(t *testing.T) {
	runner := &fakeCLIRunner{
		exitCode: 0,
		stdout:   []byte(`[{"config":"narvi/sess-1/gen-1/sandbox.json"},{"config":"narvi/sess-2/gen-3/sandbox.json"}]`),
	}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	refs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []ports.SandboxRef{
		{ProviderID: "narvi/sess-1/gen-1/sandbox.json"},
		{ProviderID: "narvi/sess-2/gen-3/sandbox.json"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("List() = %+v, want %+v", refs, want)
	}

	wantArgs := []string{"sandbox", "list", "--format", "json"}
	if got := runner.lastCall().args; !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("args = %v, want %v", got, wantArgs)
	}
}

func TestProvider_List_EmptyOutput(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0, stdout: []byte(``)}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	refs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("List() = %+v, want empty", refs)
	}
}

// TestProvider_List_Error is FIX-3's own regression test for List, mirroring
// TestProvider_StopSandbox_Error/TestProvider_CreateSandbox_Error's own
// identical shape: every existing List test until now only ever drove
// fakeCLIRunner with exitCode: 0.
func TestProvider_List_Error(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 1, stdout: []byte(`{"status":"error","error":"unauthorized"}`)}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	refs, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List() error = nil, want a ProviderError for a nonzero exit")
	}
	if refs != nil {
		t.Errorf("List() refs = %+v, want nil on error", refs)
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("List() error = %v, want *ports.ProviderError", err)
	}
	if pe.Op != ports.OpList {
		t.Errorf("error.Op = %q, want %q", pe.Op, ports.OpList)
	}
}

func TestProvider_List_MalformedJSON(t *testing.T) {
	runner := &fakeCLIRunner{exitCode: 0, stdout: []byte(`not json`)}
	p, err := newWithRunner(testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner() error = %v", err)
	}

	_, err = p.List(context.Background())
	if err == nil {
		t.Fatal("List() error = nil, want a DECODE_ERROR ProviderError")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("List() error = %v, want *ports.ProviderError", err)
	}
	if pe.Code != "DECODE_ERROR" {
		t.Errorf("error.Code = %q, want %q", pe.Code, "DECODE_ERROR")
	}
	if !pe.Transient {
		t.Error("error.Transient = false, want true (a malformed-but-0-exit output may be a transient glitch)")
	}
}

// --- helpers ---

func containsEnvEntry(env []string, key, value string) bool {
	v, ok := envValue(env, key)
	return ok && v == value
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}
