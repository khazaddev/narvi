package ports

// Capabilities reports which optional SandboxProvider operations a given
// provider actually supports (§4.1: "Capabilities() Capabilities //
// snapshots, resume, explicitStop, imageBuilds"). Callers must consult
// this before calling an optional method rather than assuming every
// provider implements every operation identically.
//
// Example split (§3.2): Modal is the snapshot-based provider ("restore =
// new gen") and reports Resume: false; a future persistent-resume
// provider (e.g. RWX, Step 48) would report Resume: true.
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
}
