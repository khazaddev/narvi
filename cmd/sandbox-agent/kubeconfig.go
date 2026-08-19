// This file (kubeconfig.go) implements Step 73b's own ("cloud identity:
// sandbox-side consumption + kubeconfig injection", §27.4) IN-SANDBOX
// half of §27.4: rendering a kubeconfig from this session's own
// Environment's cluster_bindings row (delivered alongside §27.3's own
// bindings by the SAME POST /sessions/{id}/cloud-identity-config call,
// credentials.CloudIdentityConfigDelivery.ClusterBinding), writing it
// under cloudIdentityDir (never a repo tree, this codebase's own
// established "/narvi/..." convention -- cloudidentity.go's own top doc
// comment), and returning the single "KUBECONFIG=<path>" env entry ready
// to thread into every spawned process, exactly like cloudidentity.go's
// own env entries.
//
// # The 3 auth rungs, in preference order (§27.4)
//
//  1. AuthKindCloud -- rides §27.3's ALREADY-established env vars
//     (cloudidentity.go's own applyCloudIdentityBinding output) via the
//     target cloud's own standard exec-credential plugin. Zero additional
//     secret material: the rendered kubeconfig's own `exec` stanza names
//     a COMMAND (`aws eks get-token` / `gke-gcloud-auth-plugin` /
//     `kubelogin`), never a token.
//  2. AuthKindOIDC -- the exec stanza's own command is THIS SAME binary
//     (os.Executable(), the exact `!'<binary>' credential-helper`
//     precedent internal/sandboxagent/gitclone.CredHelperGitArg already
//     established for git, applied here to kubectl's own exec-credential
//     protocol instead), invoked as `<binary> kube-credential <clientId>`
//     -- see main.go's own runKubeCredentialHelper for the subcommand
//     itself.
//  3. AuthKindStatic -- the referenced sandbox_secrets value (already
//     resolved by THIS boot's own earlier fetchSandboxSecrets call, main.go)
//     is written to cloudIdentityDir/kubeconfig VERBATIM, never templated
//     through the structs below -- §27.4: "never env-var-expanded".
//
// # Not verified against a live cluster (honest, as elsewhere)
//
// The exact exec-plugin command/argv per cloud, and the ExecCredential
// apiVersion (execAPIVersion, below) this file settles on, are a
// plausible, DOCUMENTED-not-live-verified design decision -- §27.8 itself
// leaves this level of detail unresolved ("the toolchain image... §27.4's
// three cloud exec-credential plugins... versions pinned"), and this
// codebase has no live AWS/GCP/Azure cluster to verify against during
// this Step, mirroring cloudidentity.go's own identical, stated honesty
// about its GCP external-account JSON shape.
package main

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/khazaddev/narvi/internal/domain/clusterbinding"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// kubeconfigFileName is the fixed file name (never a full path -- dir is
// always a parameter, see applyClusterBinding below) sandbox-agent
// renders/writes the kubeconfig to, for every auth rung -- under
// cloudIdentityDir (never a repo tree, cloudidentity.go's own top doc
// comment).
const kubeconfigFileName = "kubeconfig"

// execAPIVersion is the kubectl/client-go exec-credential plugin
// protocol's own API version this file declares in every rendered
// kubeconfig's `exec.apiVersion` field, and the SAME version
// runKubeCredentialHelper (main.go) emits in its own ExecCredential JSON
// output for the oidc rung -- kept as ONE named constant so the two can
// never independently drift (a kubeconfig declaring a DIFFERENT apiVersion
// than what the exec plugin actually emits is a real, documented
// client-go failure mode).
const execAPIVersion = "client.authentication.k8s.io/v1beta1"

// kubeconfigDoc is the minimal client-go kubeconfig shape this file ever
// renders -- one cluster, one context, one user, exactly the "single
// target cluster per Environment" model §27.4 itself specifies (never a
// multi-context kubeconfig).
type kubeconfigDoc struct {
	APIVersion     string                   `yaml:"apiVersion"`
	Kind           string                   `yaml:"kind"`
	Clusters       []kubeconfigNamedCluster `yaml:"clusters"`
	Contexts       []kubeconfigNamedContext `yaml:"contexts"`
	CurrentContext string                   `yaml:"current-context"`
	Users          []kubeconfigNamedUser    `yaml:"users"`
}

type kubeconfigNamedCluster struct {
	Name    string            `yaml:"name"`
	Cluster kubeconfigCluster `yaml:"cluster"`
}

type kubeconfigCluster struct {
	Server                   string `yaml:"server"`
	CertificateAuthorityData string `yaml:"certificate-authority-data"`
}

type kubeconfigNamedContext struct {
	Name    string            `yaml:"name"`
	Context kubeconfigContext `yaml:"context"`
}

type kubeconfigContext struct {
	Cluster string `yaml:"cluster"`
	User    string `yaml:"user"`
}

type kubeconfigNamedUser struct {
	Name string         `yaml:"name"`
	User kubeconfigUser `yaml:"user"`
}

type kubeconfigUser struct {
	Exec kubeconfigExec `yaml:"exec"`
}

type kubeconfigExec struct {
	APIVersion string   `yaml:"apiVersion"`
	Command    string   `yaml:"command"`
	Args       []string `yaml:"args,omitempty"`
}

// renderExecKubeconfig builds the exec-plugin-authenticated kubeconfig
// (the AuthKindCloud/AuthKindOIDC rungs share this SAME renderer, only
// their own execCommand/execArgs differ) -- name/serverURL/caBundle come
// straight from this session's own cluster_bindings row; caBundle is
// PEM text, base64-encoded here (client-go's own documented
// certificate-authority-data field shape) -- the row itself stores PEM,
// never pre-encoded, so this codebase has exactly one source of truth
// for the certificate's own bytes.
func renderExecKubeconfig(name, serverURL, caBundle, execCommand string, execArgs []string) ([]byte, error) {
	doc := kubeconfigDoc{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: []kubeconfigNamedCluster{{
			Name: name,
			Cluster: kubeconfigCluster{
				Server:                   serverURL,
				CertificateAuthorityData: base64.StdEncoding.EncodeToString([]byte(caBundle)),
			},
		}},
		Contexts: []kubeconfigNamedContext{{
			Name:    name,
			Context: kubeconfigContext{Cluster: name, User: name},
		}},
		CurrentContext: name,
		Users: []kubeconfigNamedUser{{
			Name: name,
			User: kubeconfigUser{
				Exec: kubeconfigExec{
					APIVersion: execAPIVersion,
					Command:    execCommand,
					Args:       execArgs,
				},
			},
		}},
	}
	return yaml.Marshal(doc)
}

// cloudExecCommand returns the exec plugin command+args for
// AuthKindCloud's own params.cloud value -- §27.4's own 3 named plugins.
// region is AWS-only and optional (`aws eks get-token --region`); the
// other two clouds' own plugins take no per-cluster CLI args at all in
// this codebase's own design (they resolve everything they need from the
// cluster's own kubeconfig context plus §27.3's already-set env vars).
func cloudExecCommand(cloud clusterbinding.CloudParams, clusterName string) (command string, args []string) {
	switch cloud.Cloud {
	case "aws":
		args = []string{"eks", "get-token", "--cluster-name", clusterName}
		if cloud.Region != "" {
			args = append(args, "--region", cloud.Region)
		}
		return "aws", args
	case "gcp":
		return "gke-gcloud-auth-plugin", nil
	case "azure":
		return "kubelogin", []string{"get-token", "--login", "workloadidentity"}
	default:
		// Unreachable in practice -- clusterbinding.ValidateParams (called
		// by the caller, applyClusterBinding, before this function is ever
		// reached) already rejects any OTHER cloud value.
		return "", nil
	}
}

// applyClusterBinding is run()'s own boot-time entry point for §27.4:
// renders (or, for AuthKindStatic, verbatim-copies) cluster's own
// kubeconfig to kubeconfigPath and returns the single
// "KUBECONFIG=<path>" env entry ready to thread into every spawned
// process -- nil (no env entry at all) when cluster is nil (no cluster
// configured for this session's own Environment -- the overwhelming
// common case for a session with no such Environment or no cluster
// binding yet) or when anything along the way fails (logged, Warn,
// never fatal to boot -- this feature's "warn and continue" posture
// throughout).
//
// resolvedSandboxSecrets is THIS boot's own already-fetched sandbox
// secrets map (main.go's own fetchSandboxSecrets call, which already ran
// BEFORE this function is ever invoked) -- AuthKindStatic looks its own
// params.secretName up in that SAME map rather than making a second CP
// round trip; see this file's own top doc comment for the full "never
// env-var-expanded" requirement this satisfies (the value is copied
// VERBATIM, never passed through any templating). dir is a parameter
// (never the cloudIdentityDir constant referenced directly) purely so
// this function stays testable against a temp directory -- the
// production call site (run(), main.go) always passes cloudIdentityDir.
func applyClusterBinding(cluster *credentials.CloudIdentityConfigCluster, resolvedSandboxSecrets map[string]string, dir string) []string {
	if cluster == nil {
		return nil
	}
	kubeconfigPath := filepath.Join(dir, kubeconfigFileName)

	authKind := clusterbinding.AuthKind(cluster.AuthKind)
	if !clusterbinding.IsValidAuthKind(authKind) {
		slog.Warn("sandbox-agent: cluster binding has an unrecognized auth kind, skipping kubeconfig injection", "auth_kind", cluster.AuthKind)
		return nil
	}
	if err := clusterbinding.ValidateParams(authKind, cluster.Params); err != nil {
		slog.Warn("sandbox-agent: cluster binding has invalid params, skipping kubeconfig injection", "auth_kind", cluster.AuthKind, "error", err)
		return nil
	}

	var doc []byte
	switch authKind {
	case clusterbinding.AuthKindStatic:
		var p clusterbinding.StaticParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above
		value, ok := resolvedSandboxSecrets[p.SecretName]
		if !ok {
			slog.Warn("sandbox-agent: static cluster binding references a sandbox secret that did not resolve for this session, skipping kubeconfig injection", "secret_name", p.SecretName)
			return nil
		}
		doc = []byte(value)

	case clusterbinding.AuthKindCloud:
		if cluster.ServerURL == nil || cluster.CaBundle == nil {
			slog.Warn("sandbox-agent: cloud-rung cluster binding missing serverUrl/caBundle, skipping kubeconfig injection")
			return nil
		}
		var p clusterbinding.CloudParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above
		command, args := cloudExecCommand(p, cluster.Name)
		rendered, err := renderExecKubeconfig(cluster.Name, *cluster.ServerURL, *cluster.CaBundle, command, args)
		if err != nil {
			slog.Warn("sandbox-agent: render cloud-rung kubeconfig failed, skipping kubeconfig injection", "error", err)
			return nil
		}
		doc = rendered

	case clusterbinding.AuthKindOIDC:
		if cluster.ServerURL == nil || cluster.CaBundle == nil {
			slog.Warn("sandbox-agent: oidc-rung cluster binding missing serverUrl/caBundle, skipping kubeconfig injection")
			return nil
		}
		var p clusterbinding.OIDCParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above
		exePath, err := os.Executable()
		if err != nil {
			slog.Warn("sandbox-agent: resolve own executable path failed, skipping oidc-rung kubeconfig injection", "error", err)
			return nil
		}
		rendered, err := renderExecKubeconfig(cluster.Name, *cluster.ServerURL, *cluster.CaBundle, exePath, []string{"kube-credential", p.ClientID})
		if err != nil {
			slog.Warn("sandbox-agent: render oidc-rung kubeconfig failed, skipping kubeconfig injection", "error", err)
			return nil
		}
		doc = rendered
	}

	if err := writeTokenFile(kubeconfigPath, string(doc)); err != nil {
		slog.Warn("sandbox-agent: write kubeconfig failed, skipping kubeconfig injection", "path", kubeconfigPath, "error", err)
		return nil
	}
	slog.Info("sandbox-agent: wrote kubeconfig", "auth_kind", cluster.AuthKind, "path", kubeconfigPath)
	return []string{clusterbinding.EnvVarKubeconfig + "=" + kubeconfigPath}
}
