package rwx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// rwxAccessTokenEnvVar is the exact environment variable name RWX's own
// docs specify for programmatic CLI use ("For programmatic use, set the
// token as the RWX_ACCESS_TOKEN environment variable", §4.1.1) — verified
// against RWX's own published documentation, not invented.
const rwxAccessTokenEnvVar = "RWX_ACCESS_TOKEN"

// sessionConfigEnvVar is the subprocess environment entry this adapter
// carries the opaque SESSION_CONFIG document under (§4.1: "sandbox env
// passed as one SESSION_CONFIG JSON document — the provider never
// assembles env fragments"). Deliberately a package-local name, NOT
// internal/sandboxagent/boot.SessionConfigEnvVar ("NARVI_SESSION_CONFIG")
// — this adapter, like modal.Provider before it, stops at handing
// SessionConfig to the provider's own transport as one opaque value and
// never itself confirms what env var name (if any) the provider's real
// infrastructure exposes it under inside the eventual sandbox process;
// modal.Provider makes the analogous choice by nesting SessionConfig
// under its own invented "sessionConfig" JSON field name rather than
// literally "NARVI_SESSION_CONFIG" too (modal/wire.go). Importing
// internal/sandboxagent/boot from this control-plane-side outbound
// adapter package would also cross a layering boundary no other adapter
// in this codebase crosses today.
const sessionConfigEnvVar = "SESSION_CONFIG"

// Provider is the RWX SandboxProvider adapter: it implements
// ports.SandboxProvider by shelling out to the pinned `rwx` CLI (doc.go).
// The pinned CLI's own path/name is captured only in runner (execCLIRunner.
// binary) — Provider itself never needs to read it again after
// construction, only pass it through to whichever cliRunner it holds.
type Provider struct {
	accessToken string
	timeouts    platform.Timeouts
	runner      cliRunner
}

// var _ ports.SandboxProvider = (*Provider)(nil) makes a SandboxProvider
// signature drift a build error, not a runtime surprise.
var _ ports.SandboxProvider = (*Provider)(nil)

// New constructs a Provider from cfg, validating it fail-fast (named
// errors, matching platform/config.go's/modal.New's established pattern).
// The real execCLIRunner is wired here; newWithRunner (test-only, this
// package's own white-box tests) is the one seam that substitutes a fake.
func New(cfg Config) (*Provider, error) {
	if cfg.CLIPath == "" {
		return nil, &MissingConfigError{Field: "CLIPath"}
	}
	if cfg.AccessToken == "" {
		return nil, &MissingConfigError{Field: "AccessToken"}
	}
	return newWithRunner(cfg, execCLIRunner{binary: cfg.CLIPath})
}

// newWithRunner is New's own shared constructor, taking an explicit
// cliRunner — the seam provider_test.go uses to substitute a fake without
// ever invoking a real binary. Unexported: a real caller always goes
// through New, which always wires the real execCLIRunner.
func newWithRunner(cfg Config, runner cliRunner) (*Provider, error) {
	if cfg.CLIPath == "" {
		return nil, &MissingConfigError{Field: "CLIPath"}
	}
	if cfg.AccessToken == "" {
		return nil, &MissingConfigError{Field: "AccessToken"}
	}
	return &Provider{
		accessToken: cfg.AccessToken,
		timeouts:    cfg.Timeouts,
		runner:      runner,
	}, nil
}

// Capabilities reports RWX's real, verifiably-supported capability set
// (§4.1.1).
//
//   - ExplicitStop: true — `rwx sandbox stop` is a real, documented
//     operation.
//   - Snapshots: false — RWX snapshots task filesystems into its own
//     content-addressed cache automatically, but exposes no addressable
//     take-snapshot-now/restore-from-handle API; a cache keyed by content
//     is not a SnapshotID a caller can mint and hold.
//   - ImageBuilds: false — `rwx image build|push|pull` exist, but no
//     image DELETE is documented, and this flag covers BuildImage AND
//     DeleteImage together.
//   - Resume: false — THE flag §4.1 must settle empirically, as its
//     own first exit criterion (§4.1.3), and this codebase has no real
//     RWX account reachable from its own tests/CI to settle it against
//     (the deliberate, named scope gap this Step's own landing PR
//     documents). RWX's docs imply stop→start state preservation ("the
//     sandbox persists between commands"; `reset` exists specifically to
//     discard changes — redundant if a plain stop already discarded them)
//     but never state it outright. This defaults to the CONSERVATIVE,
//     honest reading — false — deliberately NOT guessed true: with
//     Snapshots also false, this means a stopped RWX sandbox's only
//     recovery is recreate-from-scratch (§3.2's snapshot-restore lane
//     simply does not exist for RWX sessions today) — an accepted, named
//     consequence (§4.1.1), not a silent gap. Flip this to true — and
//     wire ResumeSandbox to `rwx sandbox start` against the same identity
//     instead of returning the permanent error below — only once stop→
//     start state preservation has been empirically confirmed against a
//     real RWX account; do not guess true from the docs' own implication
//     alone.
func (p *Provider) Capabilities() ports.Capabilities {
	return ports.Capabilities{
		Snapshots:    false,
		Resume:       false,
		ExplicitStop: true,
		ImageBuilds:  false,
	}
}

// rwxSubprocessEnvAllowlist is the fixed, explicit set of ambient
// environment variable names this Provider's own `rwx` CLI subprocess is
// permitted to inherit from the CONTROL PLANE's process environment -- see
// env's own doc comment below for why an ALLOWLIST, rather than the
// denylist shape internal/sandboxagent/supervisor.EnvWithout established
// for the sandbox-side gitclone/opencodeproc/boot.runHook call sites, is
// the correct shape HERE. PATH/HOME/TMPDIR cover the ordinary needs of any
// real CLI tool; HTTP_PROXY/HTTPS_PROXY/NO_PROXY (plus their lowercase
// spellings -- many non-Go HTTP clients, unlike Go's own net/http, only
// ever check the lowercase form) preserve §4.1.1's own "the subprocess
// inherits proxy env vars... so RWX traffic routes like Modal's"
// requirement; SSL_CERT_FILE/SSL_CERT_DIR preserve a corporate/dev
// TLS-interception root CA override for that same proxied traffic. A
// package-level var (not a const map literal inline in filteredAmbientEnv)
// purely so it has one obvious, greppable definition site if a future,
// deliberate widening is ever needed.
var rwxSubprocessEnvAllowlist = map[string]struct{}{
	"PATH":   {},
	"HOME":   {},
	"TMPDIR": {},

	"HTTP_PROXY":  {},
	"HTTPS_PROXY": {},
	"NO_PROXY":    {},
	"http_proxy":  {},
	"https_proxy": {},
	"no_proxy":    {},

	"SSL_CERT_FILE": {},
	"SSL_CERT_DIR":  {},
}

// filteredAmbientEnv returns osEnviron() (runner.go's own test-overridable
// seam), keeping only the entries whose key is in
// rwxSubprocessEnvAllowlist -- see env's own doc comment for the full
// rationale.
func filteredAmbientEnv() []string {
	full := osEnviron()
	out := make([]string, 0, len(full))
	for _, entry := range full {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := rwxSubprocessEnvAllowlist[key]; allowed {
			out = append(out, entry)
		}
	}
	return out
}

// env builds this Provider's own subprocess environment: an EXPLICIT
// ALLOWLIST of the ambient environment (rwxSubprocessEnvAllowlist, via
// filteredAmbientEnv, above) plus RWX_ACCESS_TOKEN, always — sessionConfig
// is appended ONLY when non-empty (StopSandbox/List need no
// SESSION_CONFIG at all; only CreateSandbox does). RWX_ACCESS_TOKEN NEVER
// travels as argv (§5.2's leak-class discipline) — this is the only place
// it is ever attached to a subprocess call.
//
// Security fix (audit finding): this used to be
// append(osEnviron(), rwxAccessTokenEnvVar+"="+p.accessToken) — the pinned
// `rwx` CLI subprocess inherited this CONTROL-PLANE process's ENTIRE
// environment, including NARVI_TOKEN_ENCRYPTION_KEY, NARVI_DATABASE_URL,
// and every OAuth/HMAC/bot secret this process holds. The old doc comment
// here justified full-env inheritance by citing gitclone's own "inherit
// the ambient environment" precedent (internal/sandboxagent/gitclone) —
// that precedent does not transfer to this adapter. gitclone (and
// opencodeproc.Spawn, and boot.runHook) run SANDBOX-side, where the
// process environment holds at most one narrow platform secret
// (NARVI_SESSION_CONFIG) — and even that one is explicitly EXCLUDED before
// spawning anything sandbox-agent does not itself control (a repo's own
// setup.sh, `opencode serve`, a services.yml command), via
// supervisor.EnvWithout(SessionConfigEnvVar) — this codebase's own
// established env-leak-to-child-processes discipline, just expressed as a
// denylist there because the sandbox-side environment has so little in it
// worth denying. This adapter instead runs CONTROL-PLANE-side, where the
// process environment holds literally every platform-wide secret this
// whole system has. A denylist shaped like EnvWithout would need to name,
// and keep naming forever as new secrets are added, every single one of
// them — silently leaking the next one anyone forgets to add. An explicit,
// closed ALLOWLIST of the small set of ambient values a CLI subprocess
// plausibly needs inverts that failure mode instead: a future secret added
// to this process's own environment is safe by default, never forwarded,
// unless someone deliberately widens rwxSubprocessEnvAllowlist.
func (p *Provider) env(sessionConfig string) []string {
	e := append(filteredAmbientEnv(), rwxAccessTokenEnvVar+"="+p.accessToken)
	if sessionConfig != "" {
		e = append(e, sessionConfigEnvVar+"="+sessionConfig)
	}
	return e
}

// run executes one `rwx` CLI invocation, bounded by
// platform.Timeouts.RWXCLIExecTimeout, and classifies any failure via
// classifyCLIError — the shared plumbing every sandbox-lifecycle method
// below uses, mirroring modal.Provider.do's own identical "one shared
// call/classify helper" shape.
func (p *Provider) run(ctx context.Context, op ports.Op, args []string, sessionConfig string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, p.timeouts.RWXCLIExecTimeout)
	defer cancel()

	stdout, stderr, exitCode, err := p.runner.Run(runCtx, args, p.env(sessionConfig))
	if exitCode == -1 {
		return nil, classifyCLIError(op, exitCode, stdout, stderr, err)
	}
	if exitCode != 0 {
		return nil, classifyCLIError(op, exitCode, stdout, stderr, nil)
	}
	return stdout, nil
}

// inactivityTimeoutFlagValue formats platform.Timeouts.
// RWXSandboxInactivityTimeout as this adapter's own `--inactivity-timeout`
// flag value — Go's own canonical time.Duration.String() format (e.g.
// "45m0s"). RWX's own exact accepted format is unverified (no real pinned
// binary is reachable from this codebase, §4.1.3-style gap) — pinned for
// real by the real-binary contract test, realbinary_test.go.
func inactivityTimeoutFlagValue(p *Provider) string {
	return p.timeouts.RWXSandboxInactivityTimeout.String()
}

// CreateSandbox implements ports.SandboxProvider: `rwx sandbox start
// --format json --config <identity> --inactivity-timeout <duration>
// [--base <spec.Image>]`, with SESSION_CONFIG carried as ONE opaque
// subprocess env entry (§4.1, never spread fragments) and RWX_ACCESS_TOKEN
// via env, never argv. The generated identity (sandboxIdentityPath,
// wire.go) embeds BOTH spec.SessionConfig.SessionId and spec.Gen, so two
// gens of the same session can never collide onto one RWX sandbox (§3.2
// fencing at the provider's own identity layer) — this SAME identity
// string becomes the returned SandboxRef.ProviderID, passed straight back
// by StopSandbox as `--config`.
func (p *Provider) CreateSandbox(ctx context.Context, spec ports.CreateSpec) (ports.SandboxRef, error) {
	if err := spec.Validate(); err != nil {
		return ports.SandboxRef{}, &ports.ProviderError{Transient: false, Code: "INVALID_SPEC", Op: ports.OpCreateSandbox, Err: err}
	}

	sessionConfigJSON, err := json.Marshal(spec.SessionConfig)
	if err != nil {
		return ports.SandboxRef{}, &ports.ProviderError{Transient: false, Code: "ENCODE_ERROR", Op: ports.OpCreateSandbox, Err: err}
	}

	identity := sandboxIdentityPath(spec.SessionConfig.SessionId, spec.Gen)
	args := []string{
		"sandbox", "start",
		"--format", "json",
		"--config", identity,
		"--inactivity-timeout", inactivityTimeoutFlagValue(p),
	}
	if spec.Image != "" {
		args = append(args, "--base", spec.Image)
	}

	if _, err := p.run(ctx, ports.OpCreateSandbox, args, string(sessionConfigJSON)); err != nil {
		return ports.SandboxRef{}, err
	}
	return ports.SandboxRef{ProviderID: identity}, nil
}

// StopSandbox implements ports.SandboxProvider: `rwx sandbox stop --format
// json --config <ref.ProviderID>` (§4.1.1: "StopSandbox: rwx sandbox
// stop").
func (p *Provider) StopSandbox(ctx context.Context, ref ports.SandboxRef) error {
	args := []string{"sandbox", "stop", "--format", "json", "--config", ref.ProviderID}
	_, err := p.run(ctx, ports.OpStopSandbox, args, "")
	return err
}

// ResumeSandbox always fails: Provider reports Capabilities().Resume ==
// false (see Capabilities' own doc comment for the full "settled
// empirically" reasoning), so this never shells out to the CLI at all —
// mirroring modal.Provider.ResumeSandbox's own established pattern: a
// caller that ignores Capabilities and calls it anyway gets a permanent,
// typed ProviderError instead of a subprocess spawn, a silent no-op, or a
// panic.
func (p *Provider) ResumeSandbox(_ context.Context, _ ports.SandboxRef) error {
	return &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpResumeSandbox,
		Err:       errResumeUnsupported,
	}
}

// TakeSnapshot always fails: Capabilities().Snapshots is false (see that
// method's own doc comment) — mirrors ResumeSandbox's own identical
// "never shells out, permanent typed error" pattern.
func (p *Provider) TakeSnapshot(_ context.Context, _ ports.SandboxRef) (ports.SnapshotID, error) {
	return "", &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpTakeSnapshot,
		Err:       errSnapshotsUnsupported,
	}
}

// RestoreFromSnapshot always fails: Capabilities().Snapshots is false —
// mirrors TakeSnapshot's own identical pattern.
func (p *Provider) RestoreFromSnapshot(_ context.Context, _ ports.SnapshotID, _ ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpRestoreFromSnapshot,
		Err:       errSnapshotsUnsupported,
	}
}

// BuildImage always fails: Capabilities().ImageBuilds is false — mirrors
// TakeSnapshot's own identical pattern. §10 Phase 2's own systematic
// fallback-to-base-on-any-miss already covers a provider that never
// builds anything (§4.1.1: RWX's own content-addressed layer cache
// already provides the warm-boot effect §19 builds by hand for Modal, so
// this costs little).
func (p *Provider) BuildImage(_ context.Context, _ ports.ImageSpec) (ports.BuildOutcome, error) {
	return ports.BuildOutcome{}, &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpBuildImage,
		Err:       errImageBuildsUnsupported,
	}
}

// DeleteImage always fails: Capabilities().ImageBuilds is false — mirrors
// BuildImage's own identical pattern.
func (p *Provider) DeleteImage(_ context.Context, _ ports.ImageRef) error {
	return &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpDeleteImage,
		Err:       errImageBuildsUnsupported,
	}
}

// List implements ports.SandboxProvider: `rwx sandbox list --format json`
// for §4.1's own reconciliation/GC (§4.1.1: "List: rwx sandbox list
// --format json"). Whether this reports org-wide truth (what
// reconciliation/GC actually needs) or only sandboxes visible to the
// calling device/user is unverified (§4.1.3's own named gap) — this
// method itself makes no such distinction; it reports exactly what the
// CLI's own JSON array names.
func (p *Provider) List(ctx context.Context) ([]ports.SandboxRef, error) {
	args := []string{"sandbox", "list", "--format", "json"}
	stdout, err := p.run(ctx, ports.OpList, args, "")
	if err != nil {
		return nil, err
	}

	var entries []cliListEntry
	if len(stdout) > 0 {
		if err := json.Unmarshal(stdout, &entries); err != nil {
			return nil, &ports.ProviderError{Transient: true, Code: "DECODE_ERROR", Op: ports.OpList, Err: fmt.Errorf("rwx: decode sandbox list output: %w", err)}
		}
	}

	refs := make([]ports.SandboxRef, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, ports.SandboxRef{ProviderID: e.Config})
	}
	return refs, nil
}
