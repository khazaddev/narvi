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
// field's own doc comment; the H6 audit fix (outbox claim-lease race) adds
// a 7th, independent link: "OutboxClaimDuration > OutboxDeliveryTimeout",
// needed for outboxworker.Builder's own per-row claim-renewal heartbeat to
// actually protect a single delivery attempt -- see OutboxClaimDuration's
// own doc comment). Each case starts from DefaultTimeouts (known-valid)
// and mutates exactly one field so exactly one link breaks, then asserts
// Validate reports a *TimeoutInvariantError naming that exact chain -- so
// the test actually catches someone breaking one specific link later, not
// merely "some error happened".
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
		{
			name: "OutboxClaimDuration not > OutboxDeliveryTimeout",
			mutate: func(to *platform.Timeouts) {
				to.OutboxClaimDuration = to.OutboxDeliveryTimeout
			},
			wantChain: "OutboxClaimDuration > OutboxDeliveryTimeout",
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

// TestDefaultTimeouts_Step44StandaloneField proves Step 44's ("OpenCode
// adapter: context-overflow compaction retry", §7.2) own addition --
// OpenCodeSummarizeTimeout -- ships with a sensible, non-zero default,
// deliberately more generous than OpenCodeRequestTimeout (the field it
// would otherwise silently inherit via doJSON's own per-request wrap, see
// this field's own doc comment), and does not disturb either invariant
// chain.
func TestDefaultTimeouts_Step44StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.OpenCodeSummarizeTimeout <= 0 {
		t.Errorf("OpenCodeSummarizeTimeout = %v, want > 0", to.OpenCodeSummarizeTimeout)
	}
	if to.OpenCodeSummarizeTimeout != 120*time.Second {
		t.Errorf("OpenCodeSummarizeTimeout = %v, want %v", to.OpenCodeSummarizeTimeout, 120*time.Second)
	}
	if to.OpenCodeSummarizeTimeout <= to.OpenCodeRequestTimeout {
		t.Errorf("OpenCodeSummarizeTimeout = %v, want strictly greater than OpenCodeRequestTimeout = %v "+
			"(a real /summarize call against a large context can plausibly run far longer than an ordinary "+
			"session/catalog/message-list call)",
			to.OpenCodeSummarizeTimeout, to.OpenCodeRequestTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_OpenCodeTransientRetryBackoffStandaloneField proves
// this Step's own ("OpenCode adapter: typed transient-error retry")
// addition -- OpenCodeTransientRetryBackoff -- ships with a sensible,
// non-zero default, is deliberately much shorter than
// OpenCodeSummarizeTimeout (a genuinely different kind of duration -- a
// short, deliberately-chosen pause this adapter itself inserts, not a
// bound on how long an external HTTP call is allowed to take -- see this
// field's own doc comment), and does not disturb either invariant chain.
func TestDefaultTimeouts_OpenCodeTransientRetryBackoffStandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.OpenCodeTransientRetryBackoff <= 0 {
		t.Errorf("OpenCodeTransientRetryBackoff = %v, want > 0", to.OpenCodeTransientRetryBackoff)
	}
	if to.OpenCodeTransientRetryBackoff != 2*time.Second {
		t.Errorf("OpenCodeTransientRetryBackoff = %v, want %v", to.OpenCodeTransientRetryBackoff, 2*time.Second)
	}
	if to.OpenCodeTransientRetryBackoff >= to.OpenCodeSummarizeTimeout {
		t.Errorf("OpenCodeTransientRetryBackoff = %v, want strictly less than OpenCodeSummarizeTimeout = %v "+
			"(a deliberately short retry pause, not a bound on a slow HTTP call)",
			to.OpenCodeTransientRetryBackoff, to.OpenCodeSummarizeTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
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

// TestDefaultTimeouts_Step33StandaloneField proves Step 33's ("Slack
// ingress") own addition -- SlackAckTimeout -- is populated with a
// sensible default and does not disturb either invariant chain, matching
// every other standalone addition's own test precedent above.
func TestDefaultTimeouts_Step33StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.SlackAckTimeout <= 0 {
		t.Errorf("SlackAckTimeout = %v, want > 0", to.SlackAckTimeout)
	}
	if to.SlackAckTimeout != 10*time.Second {
		t.Errorf("SlackAckTimeout = %v, want %v", to.SlackAckTimeout, 10*time.Second)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step35StandaloneFields proves Step 35's ("outbox
// delivery", §5.1) own additions -- OutboxPumpInterval, OutboxBackoffBase,
// OutboxBackoffMax, OutboxDeliveryTimeout, OutboxClaimDuration -- are
// populated with sensible defaults and that Validate() still returns nil.
// Retroactively added by the H6/M15/M17/L13 audit-fix batch (this family
// previously had zero TestDefaultTimeouts_* coverage of its own, despite
// the established per-Step/per-batch "StandaloneField(s)" pattern every
// other family above already follows) -- named for the Step that
// introduced the fields, per that same precedent (e.g.
// TestDefaultTimeouts_Step31StandaloneField/Step33StandaloneField above),
// not for the later audit-fix batch that finally covers them.
//
// Unlike its four siblings, OutboxClaimDuration is NOT purely standalone
// any more: the H6 audit fix (per-row claim renewal, see that field's own
// doc comment) wired it into a new, independent Validate() invariant
// against OutboxDeliveryTimeout, mirroring Step 25's own
// ReconcilerInterval/ReconcilerOrphanConfirmationPeriod precedent of a
// later fix adding one narrow pairwise check outside either named chain --
// so this test also asserts that specific relationship explicitly (not
// just "Validate() returns nil"), the same way
// TestDefaultTimeouts_Step38StandaloneField below asserts
// SlackInteractivityAckTimeout < SlackAckTimeout explicitly rather than
// only checking Validate().
func TestDefaultTimeouts_Step35StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.OutboxPumpInterval <= 0 {
		t.Errorf("OutboxPumpInterval = %v, want > 0", to.OutboxPumpInterval)
	}
	if to.OutboxPumpInterval != 5*time.Second {
		t.Errorf("OutboxPumpInterval = %v, want %v", to.OutboxPumpInterval, 5*time.Second)
	}

	if to.OutboxBackoffBase <= 0 {
		t.Errorf("OutboxBackoffBase = %v, want > 0", to.OutboxBackoffBase)
	}
	if to.OutboxBackoffBase != 30*time.Second {
		t.Errorf("OutboxBackoffBase = %v, want %v", to.OutboxBackoffBase, 30*time.Second)
	}

	if to.OutboxBackoffMax < to.OutboxBackoffBase {
		t.Errorf("OutboxBackoffMax = %v, want >= OutboxBackoffBase = %v", to.OutboxBackoffMax, to.OutboxBackoffBase)
	}
	if to.OutboxBackoffMax != 5*time.Minute {
		t.Errorf("OutboxBackoffMax = %v, want %v", to.OutboxBackoffMax, 5*time.Minute)
	}

	if to.OutboxDeliveryTimeout <= 0 {
		t.Errorf("OutboxDeliveryTimeout = %v, want > 0", to.OutboxDeliveryTimeout)
	}
	if to.OutboxDeliveryTimeout != 15*time.Second {
		t.Errorf("OutboxDeliveryTimeout = %v, want %v", to.OutboxDeliveryTimeout, 15*time.Second)
	}

	if to.OutboxClaimDuration <= 0 {
		t.Errorf("OutboxClaimDuration = %v, want > 0", to.OutboxClaimDuration)
	}
	if to.OutboxClaimDuration != 45*time.Second {
		t.Errorf("OutboxClaimDuration = %v, want %v", to.OutboxClaimDuration, 45*time.Second)
	}
	// The H6 audit-fix invariant this field's own doc comment describes:
	// a single OutboxDeliveryTimeout-bounded delivery attempt must never
	// be able to outlive the claim-renewal window attempt() just
	// refreshed to protect it, with at least MinTimeoutMargin of headroom.
	if to.OutboxClaimDuration < to.OutboxDeliveryTimeout+platform.MinTimeoutMargin {
		t.Errorf("OutboxClaimDuration = %v, want >= OutboxDeliveryTimeout (%v) + MinTimeoutMargin (%v)",
			to.OutboxClaimDuration, to.OutboxDeliveryTimeout, platform.MinTimeoutMargin)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestDefaultTimeouts_Step38StandaloneField proves Step 38's ("plan mode,
// cross-channel") own addition -- SlackInteractivityAckTimeout -- is
// populated with a sensible default, does not disturb either invariant
// chain, and is genuinely much tighter than SlackAckTimeout -- the specific
// property this field exists to fix (a confirmed adversarial-review
// finding): SlackAckTimeout (Step 33) was sized for the Events API's own
// in-thread ack, a completely different and much less time-pressured
// budget than Slack's real interactivity payload ack window (a hard ~3s),
// so reusing it for the interactivity path silently permitted the handler
// to blow well past that real budget under DB contention or a slow Slack
// response.
func TestDefaultTimeouts_Step38StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.SlackInteractivityAckTimeout <= 0 {
		t.Errorf("SlackInteractivityAckTimeout = %v, want > 0", to.SlackInteractivityAckTimeout)
	}
	if to.SlackInteractivityAckTimeout != 2500*time.Millisecond {
		t.Errorf("SlackInteractivityAckTimeout = %v, want %v", to.SlackInteractivityAckTimeout, 2500*time.Millisecond)
	}
	if to.SlackInteractivityAckTimeout >= to.SlackAckTimeout {
		t.Errorf("SlackInteractivityAckTimeout = %v, want strictly less than SlackAckTimeout = %v "+
			"(the interactivity path's real ~3s Slack budget is much tighter than the Events API in-thread ack's own budget)",
			to.SlackInteractivityAckTimeout, to.SlackAckTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step36StandaloneField proves Step 36's ("intent
// classifier", §8.3/§18) own addition -- IntentClassifierLLMTimeout -- is
// populated with a sensible default and does not disturb either invariant
// chain, matching every other standalone addition's own test precedent
// above. Retroactively added by the observability/consolidation audit-fix
// batch's own L13 finding (classifier slice): this field previously had
// NO TestDefaultTimeouts_* coverage of its own at all, unlike every other
// Step/batch-introduced field in this file.
func TestDefaultTimeouts_Step36StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.IntentClassifierLLMTimeout <= 0 {
		t.Errorf("IntentClassifierLLMTimeout = %v, want > 0", to.IntentClassifierLLMTimeout)
	}
	if to.IntentClassifierLLMTimeout != 10*time.Second {
		t.Errorf("IntentClassifierLLMTimeout = %v, want %v", to.IntentClassifierLLMTimeout, 10*time.Second)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step39StandaloneFields proves Step 39's ("identities
// + full RBAC", §13.2) own additions -- the identity profile-email fetch
// retry knobs and the identity-link-prompt TTL -- are populated with
// sensible defaults and do not disturb either invariant chain.
func TestDefaultTimeouts_Step39StandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.IdentityEmailFetchTimeout <= 0 {
		t.Errorf("IdentityEmailFetchTimeout = %v, want > 0", to.IdentityEmailFetchTimeout)
	}
	if to.IdentityEmailFetchMaxAttempts < 1 {
		t.Errorf("IdentityEmailFetchMaxAttempts = %d, want >= 1", to.IdentityEmailFetchMaxAttempts)
	}
	if to.IdentityEmailFetchRetryBaseDelay <= 0 {
		t.Errorf("IdentityEmailFetchRetryBaseDelay = %v, want > 0", to.IdentityEmailFetchRetryBaseDelay)
	}
	if to.IdentityEmailFetchRetryMaxDelay < to.IdentityEmailFetchRetryBaseDelay {
		t.Errorf("IdentityEmailFetchRetryMaxDelay = %v, want >= IdentityEmailFetchRetryBaseDelay = %v",
			to.IdentityEmailFetchRetryMaxDelay, to.IdentityEmailFetchRetryBaseDelay)
	}
	if to.IdentityLinkPromptTTL <= 0 {
		t.Errorf("IdentityLinkPromptTTL = %v, want > 0", to.IdentityLinkPromptTTL)
	}
	if to.SlackInteractivityIdentityFetchTimeout <= 0 {
		t.Errorf("SlackInteractivityIdentityFetchTimeout = %v, want > 0", to.SlackInteractivityIdentityFetchTimeout)
	}
	if to.SlackInteractivityIdentityFetchTimeout >= to.SlackInteractivityAckTimeout {
		t.Errorf("SlackInteractivityIdentityFetchTimeout = %v, want strictly less than SlackInteractivityAckTimeout = %v "+
			"(must leave real margin for the DecidePlan+chat.update calls sharing the same budget)",
			to.SlackInteractivityIdentityFetchTimeout, to.SlackInteractivityAckTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (these fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_IdentityEmailFetchWorstCaseTimingBudget is the L13
// audit fix's own ("identity slice") extension of
// TestDefaultTimeouts_Step39StandaloneFields above: that test only checks
// basic field presence/relative ordering, never the actual worst-case
// timing invariant the L5 audit fix (see IdentityEmailFetchTimeout's own
// doc comment) exists to guarantee.
//
// FetchEmailWithRetry's own retry loop (internal/app/identitylink/
// retry.go) runs SYNCHRONOUSLY, inline, on the Slack Events API webhook
// request path, BEFORE thread<->session mapping, turn creation, or the
// in-thread ack (internal/adapters/inbound/slack's own handler.go
// handleEvent, and that package's own doc.go: "a real, common occurrence
// any time this handler doesn't answer within Slack's own ~3s budget").
// This test proves the REAL worst case -- IdentityEmailFetchMaxAttempts
// attempts, EACH genuinely consuming its own IdentityEmailFetchTimeout,
// PLUS every backoff wait between them, pessimistically every wait at
// IdentityEmailFetchRetryMaxDelay (a looser bound than the smaller value
// platform.Retry's own doubling-from-base sequence would actually reach
// for these defaults) -- leaves MEANINGFUL headroom under that ~3s budget
// for the rest of handleEvent's own synchronous work in the same request.
//
// Deliberately a STANDALONE test, not a new Validate() invariant (this
// batch's own L13 finding asks for exactly this choice to be made
// explicitly): every existing Validate() pairwise check (Chain A, Chain
// B, and the two independent additions -- ReconcilerInterval/
// ReconcilerOrphanConfirmationPeriod's and OutboxClaimDuration/
// OutboxDeliveryTimeout's own) compares two Timeouts fields AGAINST EACH
// OTHER, requiring at least MinTimeoutMargin (30s) of headroom between
// them. This budget is different in kind: it compares a computed worst
// case -- built by multiplying/summing THREE Timeouts fields together,
// not comparing one field directly against another -- against an
// EXTERNAL constant (Slack's own real platform requirement, which is not
// itself a Timeouts field at all). A sub-3s budget can never clear a 30s
// margin requirement, so reusing Validate()'s own "check" helper here
// would either be meaningless against MinTimeoutMargin or require a
// SECOND, differently-scaled margin constant that exists solely for this
// one check -- a standalone test asserting the real arithmetic directly
// against that external constant says exactly what is being protected
// without distorting Validate()'s own existing, uniform contract.
//
// HIGH audit fix (IdentityEmailFetchTimeout's own doc comment): the
// underlying default values changed (300ms/3 attempts -> 800ms/2
// attempts, a realistic per-attempt budget instead of an
// arithmetic-driven one), and with them the headroom bar below. The
// PREVIOUS bar required this retry loop to consume at most HALF of
// Slack's ~3s budget -- itself just an artifact of the 300ms/3-attempts
// values being fixed at the time (1.2s worst case comfortably cleared a
// 1.5s bar). That fraction was never an independent requirement; the
// real requirement is that the REST of handleEvent's own synchronous work
// (thread<->session mapping, turn creation, the in-thread ack -- all fast,
// local Postgres operations plus one already-independently-bounded ack
// POST) has enough absolute time left, not any particular percentage of
// the total. Replaced with an absolute floor: at least 1 full second of
// headroom must remain. At the new defaults (2x800ms + 1x150ms = 1.75s
// worst case) that leaves 1.25s, comfortably clearing this floor.
func TestDefaultTimeouts_IdentityEmailFetchWorstCaseTimingBudget(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	// Slack's own real platform requirement (internal/adapters/inbound/
	// slack's own doc.go): answer the webhook within ~3s or Slack
	// redelivers it. Not a platform.Timeouts field -- an external constant
	// this package's own retry-loop budget must stay comfortably inside.
	const slackWebhookAckBudget = 3 * time.Second

	// Meaningful, absolute headroom for the rest of handleEvent's own
	// synchronous work in the same request -- see this test's own doc
	// comment above for why an absolute floor, not a fraction of the
	// budget, is the right bar now.
	const minHeadroom = 1 * time.Second

	attempts := time.Duration(to.IdentityEmailFetchMaxAttempts) * to.IdentityEmailFetchTimeout
	worstCaseBackoff := time.Duration(to.IdentityEmailFetchMaxAttempts-1) * to.IdentityEmailFetchRetryMaxDelay
	worstCase := attempts + worstCaseBackoff

	if worstCase >= slackWebhookAckBudget {
		t.Fatalf("IdentityEmailFetch worst case = %v (MaxAttempts=%d x Timeout=%v + %d waits x RetryMaxDelay=%v), want < Slack's own ~%v webhook-ack budget",
			worstCase, to.IdentityEmailFetchMaxAttempts, to.IdentityEmailFetchTimeout,
			to.IdentityEmailFetchMaxAttempts-1, to.IdentityEmailFetchRetryMaxDelay, slackWebhookAckBudget)
	}

	if headroom := slackWebhookAckBudget - worstCase; headroom < minHeadroom {
		t.Errorf("IdentityEmailFetch worst case = %v, headroom under Slack's ~%v budget = %v, want >= %v for the rest of the handler's own work",
			worstCase, slackWebhookAckBudget, headroom, minHeadroom)
	}
}

// TestDefaultTimeouts_GitHubPRPayloadCorrectnessStandaloneField proves the
// audit-remediation (completeness-vs-plan lens, GitHub PR-payload-
// correctness batch) standalone addition (GitHubGetPRTimeout) ships with a
// sane, non-zero default, and that adding it did not disturb either
// pre-existing invariant chain (it is a standalone field, wired into
// neither).
func TestDefaultTimeouts_GitHubPRPayloadCorrectnessStandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.GitHubGetPRTimeout <= 0 {
		t.Errorf("GitHubGetPRTimeout = %v, want > 0", to.GitHubGetPRTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (GitHubGetPRTimeout must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step40StandaloneField proves Step 40's ("warm boot:
// fetch-aware git sync", §19.3) own addition -- GitFetchStepTimeout -- ships
// with a sane, non-zero default matching its own documented value (§19.3's
// own explicit "propose 90s"), and that adding it did not disturb either
// pre-existing invariant chain (it is a standalone field, wired into
// neither), matching every other standalone addition's own test precedent
// above.
func TestDefaultTimeouts_Step40StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.GitFetchStepTimeout <= 0 {
		t.Errorf("GitFetchStepTimeout = %v, want > 0", to.GitFetchStepTimeout)
	}
	if to.GitFetchStepTimeout != 90*time.Second {
		t.Errorf("GitFetchStepTimeout = %v, want %v", to.GitFetchStepTimeout, 90*time.Second)
	}

	// GitFetchStepTimeout is deliberately DISTINCT from, and larger than,
	// GitSyncStepTimeout -- the new network-bound fetch step needs more
	// headroom than every other (local-only) git subprocess this package
	// spawns. This is not an enforced Validate() invariant (no ordering
	// relationship is wired for this standalone field, matching every
	// other standalone addition's own precedent), but is a genuine
	// property of the two chosen values worth pinning directly.
	if to.GitFetchStepTimeout <= to.GitSyncStepTimeout {
		t.Errorf("GitFetchStepTimeout = %v, want > GitSyncStepTimeout = %v (network-bound fetch needs more headroom than a local-only git subprocess)",
			to.GitFetchStepTimeout, to.GitSyncStepTimeout)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (GitFetchStepTimeout must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_WarmBootAccessGateStandaloneFields proves the audit
// fix ("warm-boot image access control", HIGH) standalone additions
// (RepoAccessCheckTimeout, RepoAccessCacheTTL) ship with sane, non-zero
// defaults matching their own documented values. These fields have no
// ordering relationship with either invariant chain, so this only checks
// their own values -- not Validate, which never touches them.
func TestDefaultTimeouts_WarmBootAccessGateStandaloneFields(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.RepoAccessCheckTimeout <= 0 {
		t.Errorf("RepoAccessCheckTimeout = %v, want > 0", to.RepoAccessCheckTimeout)
	}
	if to.RepoAccessCheckTimeout != 10*time.Second {
		t.Errorf("RepoAccessCheckTimeout = %v, want %v", to.RepoAccessCheckTimeout, 10*time.Second)
	}

	if to.RepoAccessCacheTTL <= 0 {
		t.Errorf("RepoAccessCacheTTL = %v, want > 0", to.RepoAccessCacheTTL)
	}
	if to.RepoAccessCacheTTL != 10*time.Minute {
		t.Errorf("RepoAccessCacheTTL = %v, want %v", to.RepoAccessCacheTTL, 10*time.Minute)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (these fields must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_Step42StandaloneField proves Step 42's ("warm boot:
// refresh pump + hook policy", §19.2) own addition -- ImageRefreshCheckInterval
// -- ships with a sane, non-zero default matching its own documented value
// (§19.2's own explicit "propose 10 min"), and that adding it did not
// disturb either pre-existing invariant chain. This field shipped with the
// original Step 42 diff but -- unlike GitFetchStepTimeout/
// OpenCodeSummarizeTimeout above -- had no standalone-field test of its own
// (an audit finding this batch closes): a NewBuilder/runRefreshPump caller
// constructing a partial Timeouts (several tests already do) would panic
// on time.NewTicker(non-positive duration) with nothing catching it.
func TestDefaultTimeouts_Step42StandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.ImageRefreshCheckInterval <= 0 {
		t.Errorf("ImageRefreshCheckInterval = %v, want > 0 (runRefreshPump's own time.NewTicker panics on a non-positive duration)", to.ImageRefreshCheckInterval)
	}
	if to.ImageRefreshCheckInterval != 10*time.Minute {
		t.Errorf("ImageRefreshCheckInterval = %v, want %v", to.ImageRefreshCheckInterval, 10*time.Minute)
	}

	// Deliberately much coarser than ImageBuildPumpInterval's own 60s --
	// see the field's own doc comment for why (a staleness-only, not
	// availability, concern).
	if to.ImageRefreshCheckInterval <= to.ImageBuildPumpInterval {
		t.Errorf("ImageRefreshCheckInterval = %v, want > ImageBuildPumpInterval = %v (refresh cadence is deliberately coarser than the build pump's own)",
			to.ImageRefreshCheckInterval, to.ImageBuildPumpInterval)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (ImageRefreshCheckInterval must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_RepoAccessCheckBreakerWindowStandaloneField proves the
// audit-remediation (correctness-availability, finding #5) standalone
// addition -- RepoAccessCheckBreakerWindow -- ships with a sane, non-zero
// default, strictly shorter than RepoAccessCacheTTL (this field damps
// repeated NETWORK CALLS during a transient SCM outage; it must never be
// mistaken for -- or accidentally sized like -- the much longer window a
// genuine access VERDICT is cached for), and does not disturb either
// invariant chain.
func TestDefaultTimeouts_RepoAccessCheckBreakerWindowStandaloneField(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.RepoAccessCheckBreakerWindow <= 0 {
		t.Errorf("RepoAccessCheckBreakerWindow = %v, want > 0", to.RepoAccessCheckBreakerWindow)
	}
	if to.RepoAccessCheckBreakerWindow != 2*time.Minute {
		t.Errorf("RepoAccessCheckBreakerWindow = %v, want %v", to.RepoAccessCheckBreakerWindow, 2*time.Minute)
	}
	if to.RepoAccessCheckBreakerWindow >= to.RepoAccessCacheTTL {
		t.Errorf("RepoAccessCheckBreakerWindow = %v, want strictly less than RepoAccessCacheTTL = %v "+
			"(damping repeated network calls during an outage is a much shorter-lived concern than caching a genuine access verdict)",
			to.RepoAccessCheckBreakerWindow, to.RepoAccessCacheTTL)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (this field must not disturb either invariant chain)", err)
	}
}

// TestDefaultTimeouts_ImageRefreshClaimStaleAfter proves audit-remediation
// batch B2's own new field (closing the imagebuild refresh-pump crash
// window, see internal/app/imagebuild/doc.go) ships with a sane, non-zero
// default, matching its own documented value, and that it is comfortably
// larger than ImageRefreshCheckInterval -- a lease bound shorter than (or
// too close to) the poll interval itself would reclaim a claim that is
// merely between ticks, not genuinely abandoned.
func TestDefaultTimeouts_ImageRefreshClaimStaleAfter(t *testing.T) {
	t.Parallel()

	to := platform.DefaultTimeouts()

	if to.ImageRefreshClaimStaleAfter <= 0 {
		t.Errorf("ImageRefreshClaimStaleAfter = %v, want > 0 (ClaimImageBuildForRefresh's own staleness comparison must be meaningful)", to.ImageRefreshClaimStaleAfter)
	}
	if to.ImageRefreshClaimStaleAfter != 30*time.Minute {
		t.Errorf("ImageRefreshClaimStaleAfter = %v, want %v", to.ImageRefreshClaimStaleAfter, 30*time.Minute)
	}
	if to.ImageRefreshClaimStaleAfter <= to.ImageRefreshCheckInterval {
		t.Errorf("ImageRefreshClaimStaleAfter = %v, want > ImageRefreshCheckInterval = %v (must comfortably outlast a normal inter-tick gap, or a claim would be reclaimed while still genuinely in flight)",
			to.ImageRefreshClaimStaleAfter, to.ImageRefreshCheckInterval)
	}

	if err := to.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (ImageRefreshClaimStaleAfter must not disturb either invariant chain)", err)
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
