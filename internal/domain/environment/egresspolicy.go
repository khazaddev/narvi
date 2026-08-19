// This file (egresspolicy.go) implements §27.6's per-Environment
// egress_policy {mode: open|allowlist, allowlist} (Step 74) -- the
// enforced half of §27.6's egress design. The cooperative half
// (HTTP_PROXY/HTTPS_PROXY/NO_PROXY, §27.1's own sandbox_secrets
// mechanism) has no domain type of its own here: it is pure env-var
// plumbing, named in the Step 74 brief precisely so nobody builds a
// parallel mechanism for it, and is explicitly NOT enforcement (any
// process can ignore an env var) -- see cmd/sandbox-agent's own
// sandboxsecrets.go, unmodified by this Step.
package environment

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// EgressMode is EgressPolicy.Mode's own closed vocabulary -- exactly the
// two values §27.6 names ("egress_policy {mode: open|allowlist, ...}").
// The empty string is EgressPolicy's own zero value, meaning "no policy
// configured at all" (see EgressPolicy's own doc comment) -- deliberately
// distinct from EgressModeOpen, a customer's own explicit choice that
// egress is unrestricted, recorded the same way mock_configured/
// contracts_path already distinguish "never configured" from "configured
// with the default" (migrations/000095_environment_docker_egress.up.sql's
// own doc comment).
type EgressMode string

const (
	// EgressModeOpen means the sandbox's own outbound network access is
	// unrestricted -- no allowlist is consulted, nothing to enforce.
	EgressModeOpen EgressMode = "open"
	// EgressModeAllowlist means outbound access is restricted to
	// EgressPolicy.Allowlist's own hosts (plus the non-negotiable floor
	// AppendAllowlistFloor appends -- see that function's own doc
	// comment), enforced at the provider substrate (§27.6).
	EgressModeAllowlist EgressMode = "allowlist"
)

// EgressPolicy is one Environment's own egress configuration (§27.6),
// carried identically in SESSION_CONFIG and, like the Docker flag, also as
// a top-level ports.CreateSpec field (see that type's own doc comment for
// the shared "deliberate-duplicate-with-Validate" discipline).
type EgressPolicy struct {
	// Mode is EgressModeOpen, EgressModeAllowlist, or the zero value ""
	// meaning "not configured" -- see EgressMode's own doc comment.
	Mode EgressMode
	// Allowlist is the set of hosts outbound traffic may reach when Mode
	// == EgressModeAllowlist; meaningless (and, per ValidateEgressPolicy,
	// invalid if non-empty) otherwise. This is the CUSTOMER's own
	// configured list ONLY, before AppendAllowlistFloor has run -- see
	// that function's own doc comment for the non-negotiable floor this
	// list does not yet include.
	Allowlist []string
}

// RequiresEnforcement reports whether p actually requires the provider
// substrate to enforce anything (§27.6: "fail-closed exactly like
// §27.5: a policy the configured provider cannot enforce refuses the
// spawn"). Mode == EgressModeOpen (or the unconfigured zero value) needs
// no enforcement at all -- every provider already defaults to
// unrestricted egress, so there is nothing for CheckSubstrateCapabilities
// to gate on. Only EgressModeAllowlist genuinely asks the provider
// substrate to do something it might not support.
func (p EgressPolicy) RequiresEnforcement() bool {
	return p.Mode == EgressModeAllowlist
}

// Sentinel errors ValidateEgressPolicy returns, wrapped by
// *InvalidEgressPolicyError so a caller can branch on the reason via
// errors.Is while still getting the offending value via errors.As --
// mirrors ValidatePathScope/ValidateContractsPath's own established
// sentinel-error house style in this package exactly.
var (
	// ErrEgressPolicyModeInvalid means Mode is neither EgressModeOpen nor
	// EgressModeAllowlist. The wire decoder (contracts/session-config,
	// contracts/rest) already enforces this closed vocabulary at
	// UnmarshalJSON time for any value arriving over the wire; this check
	// exists for every OTHER construction path (a Postgres row read back,
	// a Go literal built directly) that bypasses JSON decoding entirely.
	ErrEgressPolicyModeInvalid = errors.New("environment: egress policy mode must be exactly \"open\" or \"allowlist\"")
	// ErrEgressPolicyAllowlistOnOpen means Mode == EgressModeOpen (or the
	// unconfigured zero value) but Allowlist is non-empty -- a
	// self-contradictory value: an open policy enforces nothing, so a
	// non-empty allowlist alongside it can only be leftover/stale input,
	// never a meaningful configuration.
	ErrEgressPolicyAllowlistOnOpen = errors.New("environment: egress policy allowlist must be empty unless mode is \"allowlist\"")
	// ErrEgressPolicyAllowlistEntryEmpty means one Allowlist entry is the
	// empty string.
	ErrEgressPolicyAllowlistEntryEmpty = errors.New("environment: egress policy allowlist entry must not be empty")
	// ErrEgressPolicyAllowlistEntryShape means one Allowlist entry is
	// shaped like a URL (carries a scheme or a path) rather than a bare
	// hostname -- the provider substrate's own network controls (§27.6:
	// "Modal's own sandbox network controls") match on host, not on a URL,
	// so a scheme/path here is a caller mistake worth rejecting fail-
	// closed rather than silently never matching anything.
	ErrEgressPolicyAllowlistEntryShape = errors.New("environment: egress policy allowlist entry must be a bare hostname, not a URL")
)

// InvalidEgressPolicyError reports a single egress policy
// ValidateEgressPolicy rejected, and why.
type InvalidEgressPolicyError struct {
	// Entry is the offending Allowlist entry, when Reason concerns one
	// specific entry; empty when Reason concerns Mode itself.
	Entry string
	// Reason is one of the Err* sentinels above -- the base sentinel this
	// error unwraps to.
	Reason error
}

func (e *InvalidEgressPolicyError) Error() string {
	if e.Entry == "" {
		return fmt.Sprintf("environment: invalid egress policy: %s", e.Reason)
	}
	return fmt.Sprintf("environment: invalid egress policy allowlist entry %q: %s", e.Entry, e.Reason)
}

func (e *InvalidEgressPolicyError) Unwrap() error { return e.Reason }

// ValidateEgressPolicy validates a candidate EgressPolicy before it is
// accepted onto an Environment (§27.6). The zero value (Mode == "",
// Allowlist empty) -- "no policy configured at all" -- is always valid;
// this function only ever rejects a genuinely present, malformed value.
func ValidateEgressPolicy(p EgressPolicy) error {
	if p.Mode == "" {
		if len(p.Allowlist) > 0 {
			return &InvalidEgressPolicyError{Reason: ErrEgressPolicyAllowlistOnOpen}
		}
		return nil
	}
	if p.Mode != EgressModeOpen && p.Mode != EgressModeAllowlist {
		return &InvalidEgressPolicyError{Reason: ErrEgressPolicyModeInvalid}
	}
	if p.Mode == EgressModeOpen {
		if len(p.Allowlist) > 0 {
			return &InvalidEgressPolicyError{Reason: ErrEgressPolicyAllowlistOnOpen}
		}
		return nil
	}
	for _, entry := range p.Allowlist {
		if entry == "" {
			return &InvalidEgressPolicyError{Entry: entry, Reason: ErrEgressPolicyAllowlistEntryEmpty}
		}
		if strings.Contains(entry, "://") || strings.ContainsAny(entry, "/ \t") {
			return &InvalidEgressPolicyError{Entry: entry, Reason: ErrEgressPolicyAllowlistEntryShape}
		}
	}
	return nil
}

// AppendAllowlistFloor is §27.6's own non-negotiable allowlist floor,
// made structural rather than merely validated at input (Step 74 brief,
// point B): "a sandbox that cannot reach the control plane or clone its
// repos is not a security posture, it is a boot failure." floor is the
// caller's own already-resolved set of must-have hosts -- the CP's own
// WS/API host plus this session's own actual git hosts (computed by the
// caller, internal/app/sessionactor's own assembleSessionConfig, from
// platform.Config.PublicBaseURL and sessionRow.Repos respectively; this
// package has no I/O and cannot resolve either itself).
//
// A no-op (returns p unchanged) when p.Mode != EgressModeAllowlist -- an
// open policy enforces nothing, so there is no allowlist for a floor to
// join. Otherwise returns a NEW EgressPolicy (never mutates p.Allowlist's
// own backing array) whose Allowlist is floor's own entries first,
// followed by every entry already in p.Allowlist that is not already
// present (compared case-insensitively, matching git host-name
// conventions -- the same comparison reposource.CheckRepoHost already
// uses), deduplicated, sorted for a deterministic, easily-diffed result.
//
// This function itself does not enforce non-bypassability -- what makes
// the floor genuinely non-bypassable is that its ONE caller runs this on
// EVERY SessionConfig assembly, unconditionally, from the stored
// Environment row's own raw configured allowlist, never from a value a
// customer's own request body could hand back pre-appended: a customer
// allowlist that omits the CP host or a git host can never reach the wire
// without both being added back in, by construction, every single time.
func AppendAllowlistFloor(p EgressPolicy, floor []string) EgressPolicy {
	if p.Mode != EgressModeAllowlist {
		return p
	}

	seen := make(map[string]bool, len(floor)+len(p.Allowlist))
	merged := make([]string, 0, len(floor)+len(p.Allowlist))
	for _, host := range floor {
		key := strings.ToLower(host)
		if host == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, host)
	}
	for _, host := range p.Allowlist {
		key := strings.ToLower(host)
		if host == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, host)
	}
	sort.Strings(merged)

	return EgressPolicy{Mode: p.Mode, Allowlist: merged}
}

// HostFromURL extracts the bare host (no scheme, no port-stripping
// beyond what net/url already does, no path) from a repo clone URL or a
// public base URL -- the shared derivation both of AppendAllowlistFloor's
// own two floor inputs (the CP's own PublicBaseURL and each of a
// session's own repo clone URLs) reduce to before being handed to
// AppendAllowlistFloor. A URL that fails to parse, or parses with an
// empty host, returns ("", false) rather than an error -- this is a
// best-effort derivation for a floor that must never itself block a
// spawn (mirroring this codebase's own general "never block a spawn"
// posture, §10-P2); a caller that cannot derive a host from one repo
// simply omits that one entry from the floor rather than failing the
// whole assembly.
func HostFromURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return parsed.Host, true
}
