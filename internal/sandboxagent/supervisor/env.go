package supervisor

import (
	"os"
	"strings"
)

// EnvWithout returns the current process's own environment (os.Environ()),
// with every entry whose key exactly matches one of names removed,
// preserving relative order otherwise. This is the deliberate opposite of
// Spec.Env's own "nil means inherit everything" default: a caller uses
// this when a child has confirmed no legitimate need for one or more
// specific things sandbox-agent's own process environment happens to
// carry (most notably NARVI_SESSION_CONFIG, which carries the sandbox's
// plaintext bearer token -- see boot.SessionConfigEnvVar), while still
// preserving everything else a real, unmodified dev tool (PATH, HOME,
// locale, version-manager shims, ...) needs to run normally -- unlike a
// hand-built allowlist, which risks silently omitting something an
// external tool this package does not control (opencode, a repo's own
// setup.sh, an arbitrary services.yml command) turns out to need.
func EnvWithout(names ...string) []string {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}

	environ := os.Environ()
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if _, isExcluded := excluded[key]; isExcluded {
			continue
		}
		out = append(out, entry)
	}
	return out
}
