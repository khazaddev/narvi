package clusterbinding

// EnvVarKubeconfig is the standard, kubectl/client-go-documented env var
// naming which kubeconfig file every Kubernetes CLI/client library reads
// by default -- the ONE fixed env-var name this package's own
// kubeconfig-rendering mechanism (cmd/sandbox-agent/kubeconfig.go) sets
// on every spawned process (§27.4: "sets KUBECONFIG on every spawned
// process"). Exported, and consumed directly by BOTH
// internal/domain/sandboxsecret.ValidateName (the reservation) and
// cmd/sandbox-agent/kubeconfig.go (the injection) -- the identical
// "one exported source, two consumers, cannot drift" shape
// internal/domain/cloudidentity's own ReservedEnvVarNames establishes for
// §27.3's names (see that file's own top doc comment), applied here to
// this package's own single name.
const EnvVarKubeconfig = "KUBECONFIG"

// ReservedEnvVarNames returns every env-var name this package's own
// kubeconfig mechanism sets -- today just EnvVarKubeconfig, but a
// function (matching cloudidentity.ReservedEnvVarNames' own shape)
// rather than a bare exported slice/constant, so a future second env var
// this package might ever need to set (there is none today) costs
// ValidateName's own call site nothing to pick up.
func ReservedEnvVarNames() []string {
	return []string{EnvVarKubeconfig}
}
