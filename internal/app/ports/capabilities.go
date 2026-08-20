package ports

// Capabilities reports which optional SandboxProvider operations a given
// provider actually supports (§4.1: "Capabilities() Capabilities //
// snapshots, resume, explicitStop, imageBuilds"). Callers must consult
// this before calling an optional method rather than assuming every
// provider implements every operation identically.
//
// Example split (§3.2): Modal is the snapshot-based provider ("restore =
// new gen") and reports Resume: false; RWX (Step 57) is the second real
// SandboxProvider implementation — it also reports Resume: false today,
// pending the empirical stop→start state-preservation verification
// §4.1.3 names as §4.1's own first exit criterion (see
// internal/adapters/outbound/rwx.Provider.Capabilities' own doc comment
// for the full reasoning) — but this Capabilities struct itself already
// supports a future provider reporting Resume: true once that or any
// other persistent-resume provider is verified.
type Capabilities struct {
	// Snapshots reports whether TakeSnapshot/RestoreFromSnapshot are
	// supported.
	Snapshots bool

	// Resume reports whether ResumeSandbox is supported (§3.2:
	// "stopped|stale + resume-capable -> resume (same provider
	// sandbox)"). A provider reporting false here must make
	// ResumeSandbox return a permanent ProviderError rather than a
	// silent no-op — see SandboxProvider.ResumeSandbox.
	Resume bool

	// ExplicitStop reports whether StopSandbox is a real, explicit
	// provider operation (as opposed to the provider relying solely on
	// its own hard TTL to reclaim instances).
	ExplicitStop bool

	// ImageBuilds reports whether BuildImage/DeleteImage are supported.
	ImageBuilds bool

	// DockerInSandbox reports whether CreateSandbox/RestoreFromSnapshot
	// honor CreateSpec.Docker (§27.5): a real, isolated dockerd
	// can run inside a spawned sandbox instance. Consulted independently
	// at two points -- once up front at session-creation time
	// (internal/adapters/inbound/httpapi.CreateSessionCore, refusing a
	// docker-requiring session outright when false) and again at dispatch
	// time (internal/app/sessionactor.tryPlanSpawn, immediately before a
	// real spawn/restore/resume attempt) -- via the SAME pure decision,
	// internal/domain/environment.CheckSubstrateCapabilities, so a
	// docker-requiring session can never be silently run somewhere this
	// requirement is unenforceable. Modal reports true (it maps the flag
	// onto its own VM runtime option, §27.5's "Modal concretely"); a
	// provider with no such option must report false, never silently
	// accept the flag and ignore it.
	DockerInSandbox bool

	// EgressPolicy reports whether CreateSandbox/RestoreFromSnapshot honor
	// CreateSpec.EgressPolicy, enforcing it at the provider's own network
	// substrate (§27.6: "Modal's own sandbox network controls;
	// NetworkPolicy for the anticipated Kubernetes provider"). Consulted
	// the SAME two-point way DockerInSandbox is -- see that field's own
	// doc comment -- but only when the policy actually requires
	// enforcement (EgressPolicy.RequiresEnforcement(), i.e. mode ==
	// allowlist): an "open" policy needs no substrate support at all,
	// since every provider already defaults to unrestricted egress.
	EgressPolicy bool
}
