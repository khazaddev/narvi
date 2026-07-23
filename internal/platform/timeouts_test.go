package platform_test

import (
	"errors"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestDefaultTimeouts_Valid proves the real shipped defaults actually
// satisfy every invariant chain -- not just that Validate works on
// contrived inputs.
func TestDefaultTimeouts_Valid(t *testing.T) {
	t.Parallel()

	if err := platform.DefaultTimeouts().Validate(); err != nil {
		t.Fatalf("DefaultTimeouts().Validate() = %v, want nil", err)
	}
}

// TestValidate_CatchesEachBrokenLink is table-driven over every pairwise
// relationship Validate checks (§5.4's "provider cap > supervisor >
// bridge > SSE" chain contributes 3 adjacent links; the independent
// "providerHTTPClientTimeout > cold start" and "first_connect_budget >
// image pull + boot p99" pairs contribute one link each -- 5 links total
// from the 8-field design in the original PR-02; Step 25's own reconciler
// orphan-GC debounce fix adds a 6th, independent link:
// "ReconcilerInterval > ReconcilerOrphanConfirmationPeriod", needed for
// app/reconciler.Reconciler's own "confirmed on the SECOND consecutive
// tick" guarantee to actually hold under the shipped defaults -- see that
// field's own doc comment). Each case starts from DefaultTimeouts
// (known-valid) and mutates exactly one field so exactly one link breaks,
// then asserts Validate reports a *TimeoutInvariantError naming that
// exact chain -- so the test actually catches someone breaking one
// specific link later, not merely "some error happened".
func TestValidate_CatchesEachBrokenLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*platform.Timeouts)
		wantChain string
	}{
		{
			name: "ProviderHardCap not > SupervisorTurnCap",
			mutate: func(to *platform.Timeouts) {
				to.SupervisorTurnCap = to.ProviderHardCap
			},
			wantChain: "ProviderHardCap > SupervisorTurnCap",
		},
		{
			name: "SupervisorTurnCap not > TurnDeadline",
			mutate: func(to *platform.Timeouts) {
				to.TurnDeadline = to.SupervisorTurnCap
			},
			wantChain: "SupervisorTurnCap > TurnDeadline",
		},
		{
			name: "TurnDeadline not > SSEInactivityTimeout",
			mutate: func(to *platform.Timeouts) {
				to.SSEInactivityTimeout = to.TurnDeadline
			},
			wantChain: "TurnDeadline > SSEInactivityTimeout",
		},
		{
			name: "ProviderHTTPClientTimeout not > ProviderWorstColdStart",
			mutate: func(to *platform.Timeouts) {
				to.ProviderWorstColdStart = to.ProviderHTTPClientTimeout
			},
			wantChain: "ProviderHTTPClientTimeout > ProviderWorstColdStart",
		},
		{
			name: "FirstConnectBudget not > ImagePullBootP99",
			mutate: func(to *platform.Timeouts) {
				to.ImagePullBootP99 = to.FirstConnectBudget
			},
			wantChain: "FirstConnectBudget > ImagePullBootP99",
		},
		{
			name: "ReconcilerInterval not > ReconcilerOrphanConfirmationPeriod",
			mutate: func(to *platform.Timeouts) {
				to.ReconcilerOrphanConfirmationPeriod = to.ReconcilerInterval
			},
			wantChain: "ReconcilerInterval > ReconcilerOrphanConfirmationPeriod",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			to := platform.DefaultTimeouts()
			tc.mutate(&to)

			err := to.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for broken link %q", tc.wantChain)
			}

			var invErr *platform.TimeoutInvariantError
			if !errors.As(err, &invErr) {
				t.Fatalf("Validate() = %v, want a *TimeoutInvariantError in the chain", err)
			}
			if invErr.Chain != tc.wantChain {
				t.Fatalf("TimeoutInvariantError.Chain = %q, want %q", invErr.Chain, tc.wantChain)
			}
		})
	}
}

// TestDefaultTimeouts_StandaloneFields proves the PR-06 standalone
// additions (HMACWindow, ShutdownGracePeriod, HealthCheckTimeout) ship with
// sane, non-zero defaults. These fields have no ordering relationship with
// either invariant chain (§ scope note on the struct), so this only checks
// their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.HMACWindow != 5*time.Minute {
		t.Errorf("HMACWindow = %v, want %v (§5.2, explicit)", to.HMACWindow, 5*time.Minute)
	}
	if to.ShutdownGracePeriod <= 0 {
		t.Errorf("ShutdownGracePeriod = %v, want > 0", to.ShutdownGracePeriod)
	}
	if to.HealthCheckTimeout <= 0 {
		t.Errorf("HealthCheckTimeout = %v, want > 0", to.HealthCheckTimeout)
	}
}

// TestDefaultTimeouts_Step07StandaloneFields proves the Step 07 standalone
// additions (sandbox liveness/circuit-breaker/spawn/inactivity fields) ship
// with the exact values §3.2 specifies (or, where §3.2 gives no figure, the
// chosen default documented alongside the field). These fields have no
// ordering relationship with either invariant chain, so this only checks
// their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_Step07StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"SteadyHeartbeatBudget", to.SteadyHeartbeatBudget, 90 * time.Second},
		{"TerminalGracePeriod", to.TerminalGracePeriod, 60 * time.Second},
		{"CircuitBreakerWindow", to.CircuitBreakerWindow, 5 * time.Minute},
		{"SpawnCooldown", to.SpawnCooldown, 30 * time.Second},
		{"SpawnReadyWait", to.SpawnReadyWait, 60 * time.Second},
		{"SpawnStuckTimeout", to.SpawnStuckTimeout, 120 * time.Second},
		{"InactivityTimeout", to.InactivityTimeout, 10 * time.Minute},
		{"InactivityExtension", to.InactivityExtension, 5 * time.Minute},
		{"InactivityMinCheckInterval", to.InactivityMinCheckInterval, 30 * time.Second},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestDefaultTimeouts_Step11StandaloneFields proves the Step 11 standalone
// additions (ActorIdleTTL, TimerPumpInterval, TimerClaimDuration) ship
// with the exact values §2 specifies (or, where §2 gives no figure, the
// chosen default documented alongside the field). These fields have no
// ordering relationship with either invariant chain, so this only checks
// their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_Step11StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ActorIdleTTL", to.ActorIdleTTL, 30 * time.Minute},
		{"TimerPumpInterval", to.TimerPumpInterval, 5 * time.Second},
		{"TimerClaimDuration", to.TimerClaimDuration, 30 * time.Second},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestDefaultTimeouts_Step13StandaloneFields proves the Step 13 standalone
// additions (HookTimeout, ProcessStopGracePeriod, SupervisorShutdownTimeout,
// RepoSHADiscoveryTimeout) ship with sane, non-zero defaults. These fields
// have no ordering relationship with either invariant chain, so this only
// checks their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_Step13StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"HookTimeout", to.HookTimeout, 10 * time.Minute},
		{"ProcessStopGracePeriod", to.ProcessStopGracePeriod, 10 * time.Second},
		{"SupervisorShutdownTimeout", to.SupervisorShutdownTimeout, 30 * time.Second},
		{"RepoSHADiscoveryTimeout", to.RepoSHADiscoveryTimeout, 5 * time.Second},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestDefaultTimeouts_Step14StandaloneFields proves the Step 14 standalone
// additions (ServiceReadinessTimeout, ServiceReadinessPollInterval) ship
// with sane, non-zero defaults. These fields have no ordering relationship
// with either invariant chain, so this only checks their own values -- not
// Validate, which never touches them.
func TestDefaultTimeouts_Step14StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ServiceReadinessTimeout", to.ServiceReadinessTimeout, 30 * time.Second},
		{"ServiceReadinessPollInterval", to.ServiceReadinessPollInterval, 250 * time.Millisecond},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestDefaultTimeouts_Step15StandaloneFields proves the Step 15 standalone
// additions (RepoCloneTimeout, CredentialFetchTimeout,
// CredentialExpiryBuffer) ship with sane, non-zero defaults. These fields
// have no ordering relationship with either invariant chain, so this only
// checks their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_Step15StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"RepoCloneTimeout", to.RepoCloneTimeout, 5 * time.Minute},
		{"CredentialFetchTimeout", to.CredentialFetchTimeout, 10 * time.Second},
		{"CredentialExpiryBuffer", to.CredentialExpiryBuffer, 5 * time.Minute},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestDefaultTimeouts_Step18StandaloneFields proves the Step 18 standalone
// addition (SandboxEventAckTimeout) ships with a sane, non-zero default,
// and that adding it did not accidentally break either pre-existing
// invariant chain (it is a standalone field, wired into neither).
func TestDefaultTimeouts_Step18StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.SandboxEventAckTimeout <= 0 {
		t.Errorf("SandboxEventAckTimeout = %v, want > 0", to.SandboxEventAckTimeout)
	}
	if to.SandboxEventAckTimeout != 5*time.Second {
		t.Errorf("SandboxEventAckTimeout = %v, want %v", to.SandboxEventAckTimeout, 5*time.Second)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (SandboxEventAckTimeout must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step21StandaloneFields proves the Step 21 ("e2e
// happy path") standalone additions (SandboxCommandSendTimeout,
// ScmCredentialTTL, PRCreateTimeout) ship with sane, non-zero defaults,
// and that adding them did not accidentally break either pre-existing
// invariant chain (all three are standalone fields, wired into neither).
func TestDefaultTimeouts_Step21StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.SandboxCommandSendTimeout <= 0 {
		t.Errorf("SandboxCommandSendTimeout = %v, want > 0", to.SandboxCommandSendTimeout)
	}
	if to.ScmCredentialTTL <= 0 {
		t.Errorf("ScmCredentialTTL = %v, want > 0", to.ScmCredentialTTL)
	}
	if to.PRCreateTimeout <= 0 {
		t.Errorf("PRCreateTimeout = %v, want > 0", to.PRCreateTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (Step 21 fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_OpenCodeTurnCompletionStandaloneFields proves the
// audit-remediation (outbound-adapters lens, turn-completion batch)
// standalone additions (OpenCodeSSEReconnectInterval, OpenCodeRequestTimeout)
// ship with sane, non-zero defaults -- and that OpenCodeSSEReconnectInterval
// is genuinely much shorter than SSEInactivityTimeout, the specific
// property Adapter.runEventLoop's own reconnect-vs-fallback race fix
// depends on. These fields have no ordering relationship with either
// invariant chain, so this only checks their own values -- not Validate,
// which never touches them.
func TestDefaultTimeouts_OpenCodeTurnCompletionStandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.OpenCodeSSEReconnectInterval <= 0 {
		t.Errorf("OpenCodeSSEReconnectInterval = %v, want > 0", to.OpenCodeSSEReconnectInterval)
	}
	if to.OpenCodeSSEReconnectInterval != 2*time.Second {
		t.Errorf("OpenCodeSSEReconnectInterval = %v, want %v", to.OpenCodeSSEReconnectInterval, 2*time.Second)
	}
	if to.OpenCodeSSEReconnectInterval >= to.SSEInactivityTimeout {
		t.Errorf("OpenCodeSSEReconnectInterval = %v, want strictly less than SSEInactivityTimeout = %v "+
			"(reconnection must have a real chance to beat the per-turn fallback)",
			to.OpenCodeSSEReconnectInterval, to.SSEInactivityTimeout)
	}

	if to.OpenCodeRequestTimeout <= 0 {
		t.Errorf("OpenCodeRequestTimeout = %v, want > 0", to.OpenCodeRequestTimeout)
	}
	if to.OpenCodeRequestTimeout != 30*time.Second {
		t.Errorf("OpenCodeRequestTimeout = %v, want %v", to.OpenCodeRequestTimeout, 30*time.Second)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (these fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_InboundHygieneStandaloneFields proves the
// audit-remediation (inbound-hygiene lens, WS/REST hygiene batch)
// standalone additions (ClientWSPingInterval, ClientFetchHistoryMinInterval)
// ship with sane, non-zero defaults matching their own documented values.
// These fields have no ordering relationship with either invariant chain,
// so this only checks their own values -- not Validate, which never
// touches them.
func TestDefaultTimeouts_InboundHygieneStandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.ClientWSPingInterval <= 0 {
		t.Errorf("ClientWSPingInterval = %v, want > 0", to.ClientWSPingInterval)
	}
	if to.ClientWSPingInterval != 30*time.Second {
		t.Errorf("ClientWSPingInterval = %v, want %v (matches SandboxWSHeartbeatInterval's own cadence, §6.1)", to.ClientWSPingInterval, 30*time.Second)
	}

	if to.ClientFetchHistoryMinInterval <= 0 {
		t.Errorf("ClientFetchHistoryMinInterval = %v, want > 0", to.ClientFetchHistoryMinInterval)
	}
	if to.ClientFetchHistoryMinInterval != 250*time.Millisecond {
		t.Errorf("ClientFetchHistoryMinInterval = %v, want %v", to.ClientFetchHistoryMinInterval, 250*time.Millisecond)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (these fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_ExpiredCredentialCleanupStandaloneField proves the
// audit-remediation (outbound-adapters lens, config/platform-hardening
// batch) standalone addition (ExpiredCredentialCleanupInterval) ships with
// a sane, non-zero default matching its own documented value. This field
// has no ordering relationship with either invariant chain, so this only
// checks its own value -- not Validate, which never touches it.
func TestDefaultTimeouts_ExpiredCredentialCleanupStandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.ExpiredCredentialCleanupInterval <= 0 {
		t.Errorf("ExpiredCredentialCleanupInterval = %v, want > 0", to.ExpiredCredentialCleanupInterval)
	}
	if to.ExpiredCredentialCleanupInterval != time.Hour {
		t.Errorf("ExpiredCredentialCleanupInterval = %v, want %v", to.ExpiredCredentialCleanupInterval, time.Hour)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step26StandaloneFields proves the Step 26 ("image
// builds") standalone additions ship with sane, non-zero defaults matching
// their own documented values, and that ImageBuildBackoffBase is strictly
// less than ImageBuildBackoffMax (the property domain/imagebuild.
// EvaluateBackoff's own exponential-growth-then-plateau schedule depends
// on). These fields have no ordering relationship with either invariant
// chain, so this only checks their own values -- not Validate, which never
// touches them.
func TestDefaultTimeouts_Step26StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.RepoSHAResolutionTimeout <= 0 {
		t.Errorf("RepoSHAResolutionTimeout = %v, want > 0", to.RepoSHAResolutionTimeout)
	}
	if to.RepoSHAResolutionTimeout != 10*time.Second {
		t.Errorf("RepoSHAResolutionTimeout = %v, want %v", to.RepoSHAResolutionTimeout, 10*time.Second)
	}

	if to.ImageBuildPumpInterval <= 0 {
		t.Errorf("ImageBuildPumpInterval = %v, want > 0", to.ImageBuildPumpInterval)
	}
	if to.ImageBuildPumpInterval != 60*time.Second {
		t.Errorf("ImageBuildPumpInterval = %v, want %v", to.ImageBuildPumpInterval, 60*time.Second)
	}

	if to.ImageBuildBackoffBase <= 0 {
		t.Errorf("ImageBuildBackoffBase = %v, want > 0", to.ImageBuildBackoffBase)
	}
	if to.ImageBuildBackoffMax <= 0 {
		t.Errorf("ImageBuildBackoffMax = %v, want > 0", to.ImageBuildBackoffMax)
	}
	if to.ImageBuildBackoffBase >= to.ImageBuildBackoffMax {
		t.Errorf("ImageBuildBackoffBase = %v, want strictly less than ImageBuildBackoffMax = %v "+
			"(the schedule must actually grow before plateauing)",
			to.ImageBuildBackoffBase, to.ImageBuildBackoffMax)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (Step 26 fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step27StandaloneFields proves the Step 27 ("mocking +
// contract drift") standalone addition ships with a sane, non-zero default
// matching its own documented value. This field has no ordering
// relationship with either invariant chain, so this only checks its own
// value -- not Validate, which never touches it.
func TestDefaultTimeouts_Step27StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.ContractsFingerprintResolutionTimeout <= 0 {
		t.Errorf("ContractsFingerprintResolutionTimeout = %v, want > 0", to.ContractsFingerprintResolutionTimeout)
	}
	if to.ContractsFingerprintResolutionTimeout != 10*time.Second {
		t.Errorf("ContractsFingerprintResolutionTimeout = %v, want %v", to.ContractsFingerprintResolutionTimeout, 10*time.Second)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step31StandaloneField proves Step 31's ("webhook
// toolkit") own addition -- WebhookTimestampFreshnessWindow -- is
// populated with a sensible default and does not disturb either
// invariant chain, matching every other standalone addition's own test
// precedent above.
func TestDefaultTimeouts_Step31StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.WebhookTimestampFreshnessWindow <= 0 {
		t.Errorf("WebhookTimestampFreshnessWindow = %v, want > 0", to.WebhookTimestampFreshnessWindow)
	}
	if to.WebhookTimestampFreshnessWindow != 5*time.Minute {
		t.Errorf("WebhookTimestampFreshnessWindow = %v, want %v", to.WebhookTimestampFreshnessWindow, 5*time.Minute)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestValidate_ReportsAllViolations proves Validate collects every broken
// link (via errors.Join) rather than stopping at the first one.
func TestValidate_ReportsAllViolations(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()
	to.SupervisorTurnCap = to.ProviderHardCap   // breaks link 1
	to.ImagePullBootP99 = to.FirstConnectBudget // breaks link 5 (independent chain)

	err := to.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Validate() error %v does not support errors.Join unwrapping", err)
	}

	gotChains := map[string]bool{}
	for _, e := range joined.Unwrap() {
		var invErr *platform.TimeoutInvariantError
		if errors.As(e, &invErr) {
			gotChains[invErr.Chain] = true
		}
	}

	for _, want := range []string{
		"ProviderHardCap > SupervisorTurnCap",
		"FirstConnectBudget > ImagePullBootP99",
	} {
		if !gotChains[want] {
			t.Errorf("Validate() did not report violated chain %q; got chains: %v", want, gotChains)
		}
	}
	if len(gotChains) != 2 {
		t.Errorf("Validate() reported %d distinct violated chains, want exactly 2 (got: %v)", len(gotChains), gotChains)
	}
}
