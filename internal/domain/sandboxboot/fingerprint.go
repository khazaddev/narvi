package sandboxboot

// BootFingerprint is the plain data shape §5.3 requires sandbox-agent log
// first, before any other line: "binary version, image digest, repo SHAs,
// boot mode". This type carries no validation or behavior of its own --
// BootMode is already validated by ParseBootMode before it ever reaches
// here, and ImageDigest/RepoSHAs are inherently best-effort (neither §6.4
// nor §5.3 gives fallback semantics beyond "log whatever's known").
// Assembly from live inputs (env vars, best-effort git plumbing) is impure
// and lives in internal/sandboxagent/boot instead.
type BootFingerprint struct {
	AgentVersion string
	ImageDigest  string
	BootMode     BootMode

	// RepoSHAs is best-effort: a repo whose HEAD SHA could not be
	// determined is simply omitted, so this may be empty. A nil map and
	// an empty map are not meaningfully different here -- keep it simple,
	// callers should not distinguish them.
	RepoSHAs map[string]string
}
