// This file (kubeconfig.go) implements §27.4's own ("cloud identity:
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
//  2. AuthKindOIDC -- NOT an exec plugin (see "Design correction" below):
//     applyClusterBinding mints this rung's own token the SAME way §27.3
//     mints every cloud_identity_bindings token (mintCloudIdentityToken,
//     cloudidentity.go, `aud` = the cluster's configured client id),
//     writes it to oidcTokenFileName under dir, and renders a kubeconfig
//     whose `users[].user.tokenFile` names that file's path directly --
//     client-go's own documented tokenFile mechanism (kubectl/client-go's
//     tools/clientcmd/api.AuthInfo.TokenFile: "periodically read... the
//     last successfully read content is used as the bearer token") reads
//     the CURRENT file content on every request, not a value cached once
//     at process start. The returned token file is registered with the
//     SAME half-life background refresh loop (runCloudIdentityRefreshLoop)
//     every §27.3 cloud_identity_bindings token already uses -- see
//     applyClusterBinding's own doc comment for the full wiring.
//  3. AuthKindStatic -- the referenced sandbox_secrets value (already
//     resolved by THIS boot's own earlier fetchSandboxSecrets call, main.go)
//     is written to cloudIdentityDir/kubeconfig VERBATIM, never templated
//     through the structs below -- §27.4: "never env-var-expanded".
//
// # Design correction: rung 2 was originally an exec plugin, and shipped
// # structurally non-functional (adversarial-review HIGH fix)
//
// The FIRST version of this Step gave AuthKindOIDC its own exec plugin --
// this SAME binary, re-invoked as `<binary> kube-credential <clientId>`,
// printing a client-go ExecCredential document -- explicitly modeled on
// gitclone's own git-credential-helper precedent
// (internal/sandboxagent/credentials, runCredentialHelper, main.go): "the
// exact same shape... git's helper protocol there, client-go's here".
// That analogy does not hold, and the rung it produced could never work:
// runKubeCredentialHelper (like runCredentialHelper) needed
// NARVI_SESSION_CONFIG (via boot.Load()) to mint anything, but EVERY
// process that can ever run `kubectl` -- opencodeproc.Spawn, boot/
// hooks.go's runHook, boot/runboot.go's services.yml dispatch -- builds
// its child env via supervisor.EnvWithout(SessionConfigEnvVar)
// specifically BECAUSE that var carries this sandbox's own live bearer
// token, which authenticates scm-credentials, provider-credentials,
// uploads, verdict-posting, and cloud-identity minting -- handing it to
// arbitrary model-authored shell commands is the exact class of leak
// those three call sites exist to prevent. The precedent's own
// distinguishing fact: sandbox-agent spawns `git` ITSELF (gitclone's own
// cloneOne/pushOneRepo Spawn calls, full env inherited on purpose -- see
// their own doc comments), so git's credential-helper re-exec inherits
// whatever env sandbox-agent gave IT, unrelated to the stripped tree.
// sandbox-agent never spawns `kubectl` -- only a model-authored command
// inside the ALREADY-stripped opencode/hook tree ever does -- so there is
// no environment for an exec plugin re-invocation to inherit
// NARVI_SESSION_CONFIG FROM. Every `oidc` cluster binding therefore
// failed 100% of the time, with `kubectl` reporting "NARVI_SESSION_CONFIG
// is unset" on its very first exec-plugin invocation. Fixed here by
// dropping the exec plugin/`kube-credential` subcommand for this rung
// entirely, in favor of the tokenFile mechanism above -- no new IPC
// surface, no new env var, and the oidc rung becomes structurally
// identical to a §27.3 cloud_identity_bindings token: a file under
// cloudIdentityDir that sandbox-agent refreshes and the consumer (here,
// client-go, via kubeconfig's own tokenFile field) re-reads on its own.
// docs/TECHNICAL_PLAN.md §27.4 rung 2 and §27.3 are corrected in the
// SAME commit as this fix, for the same
// "a future reader must not re-derive the broken design from a stale
// spec" reason cloudidentity.go's own gap-1/gap-2 write-ups exist.
//
// # Not verified against a live cluster (honest, as elsewhere)
//
// The exact exec-plugin command/argv per cloud, and the ExecCredential
// apiVersion (execAPIVersion, below -- still used by the AuthKindCloud
// rung's own exec stanza, unaffected by the correction above), are a
// plausible, DOCUMENTED-not-live-verified design decision -- §27.8 itself
// leaves this level of detail unresolved ("the toolchain image... §27.4's
// three cloud exec-credential plugins... versions pinned"), and this
// codebase has no live AWS/GCP/Azure cluster to verify against during
// this Step, mirroring cloudidentity.go's own identical, stated honesty
// about its GCP external-account JSON shape. The tokenFile re-read
// behavior above, by contrast, WAS verified directly against
// kubernetes/client-go's own published source (not assumed, not merely
// documented): transport/round_trippers.go's
// NewBearerAuthWithRefreshRoundTripper wraps a bearerAuthRoundTripper
// whose RoundTrip calls source.Token() on EVERY request (not once, at
// construction); transport/token_source.go's fileTokenSource.Token() does
// a plain os.ReadFile(ts.path) on every call it is asked to actually
// refresh, and the wrapping cachingTokenSource re-triggers that read once
// its own cached copy is more than period-leeway (1min-10s = 50s) old --
// i.e. client-go re-reads this file from disk at least once a minute
// under active use, never merely once per process. tools/clientcmd/
// api/types.go's own AuthInfo.TokenFile doc comment states the same
// thing in kubeconfig's own terms ("periodically read"), and
// tools/clientcmd/client_config.go's buildFullMergedConfig wires that
// field straight into rest.Config.BearerTokenFile (the field
// HTTPWrappersForConfig branches on to build the refreshing round
// tripper above) whenever a kubeconfig sets tokenFile without also
// setting a literal token.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/narvidev/narvi/internal/domain/clusterbinding"
	"github.com/narvidev/narvi/internal/platform"
	"github.com/narvidev/narvi/internal/sandboxagent/credentials"
)

// kubeconfigFileName is the fixed file name (never a full path -- dir is
// always a parameter, see applyClusterBinding below) sandbox-agent
// renders/writes the kubeconfig to, for every auth rung -- under
// cloudIdentityDir (never a repo tree, cloudidentity.go's own top doc
// comment).
const kubeconfigFileName = "kubeconfig"

// oidcTokenFileName is the file name (never a full path -- dir is always
// a parameter, see applyClusterBinding below) sandbox-agent writes the
// §27.4 AuthKindOIDC cluster binding's own minted token to, under
// cloudIdentityDir -- named distinctly from kubeconfigFileName (the
// rendered kubeconfig itself, whose own `tokenFile` field points AT this
// file's path) and from cloudidentity.go's own awsTokenFileName/
// gcpTokenFileName/etc. (a different table, cloud_identity_bindings, not
// this file's own single cluster_bindings row).
const oidcTokenFileName = "oidc-token"

// execAPIVersion is the kubectl/client-go exec-credential plugin
// protocol's own API version this file declares in every rendered
// AuthKindCloud kubeconfig's own `exec.apiVersion` field (renderExecKubeconfig,
// below) -- the ONLY rung that still uses the exec-plugin mechanism at all
// (see this file's own top doc comment's "Design correction" section for
// why AuthKindOIDC no longer does). A single named constant, rather than
// a literal at renderExecKubeconfig's own call site, purely so a future
// version bump has exactly one place to change.
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

// kubeconfigUser carries EITHER Exec (the AuthKindCloud rung's own
// exec-credential plugin) OR TokenFile (the AuthKindOIDC rung, since the
// "Design correction" section of this file's own top doc comment) --
// never both for the same rendered document. Exec is a pointer
// specifically so `omitempty` genuinely omits the whole `exec:` stanza
// for a tokenFile-authenticated user (a zero-value, non-pointer
// kubeconfigExec would still marshal as `exec: {}`, which client-go
// treats as "use the exec plugin mechanism" regardless of its own empty
// fields -- structurally wrong for this rung).
type kubeconfigUser struct {
	Exec      *kubeconfigExec `yaml:"exec,omitempty"`
	TokenFile string          `yaml:"tokenFile,omitempty"`
}

type kubeconfigExec struct {
	APIVersion string   `yaml:"apiVersion"`
	Command    string   `yaml:"command"`
	Args       []string `yaml:"args,omitempty"`
}

// buildKubeconfigDoc builds the single-cluster/single-context/single-user
// kubeconfigDoc shape every rung of this file renders (the "one cluster,
// one context, one user" model kubeconfigDoc's own doc comment names) --
// name/serverURL/caBundle come straight from this session's own
// cluster_bindings row; caBundle is PEM text, base64-encoded here
// (client-go's own documented certificate-authority-data field shape) --
// the row itself stores PEM, never pre-encoded, so this codebase has
// exactly one source of truth for the certificate's own bytes. user is
// the ONE field that actually differs by auth rung (renderExecKubeconfig/
// renderTokenFileKubeconfig, below, each build their own).
func buildKubeconfigDoc(name, serverURL, caBundle string, user kubeconfigUser) kubeconfigDoc {
	return kubeconfigDoc{
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
		Users:          []kubeconfigNamedUser{{Name: name, User: user}},
	}
}

// renderExecKubeconfig builds the exec-plugin-authenticated kubeconfig --
// the AuthKindCloud rung's own renderer (see this file's own top doc
// comment's "Design correction" section for why AuthKindOIDC no longer
// shares this function).
func renderExecKubeconfig(name, serverURL, caBundle, execCommand string, execArgs []string) ([]byte, error) {
	doc := buildKubeconfigDoc(name, serverURL, caBundle, kubeconfigUser{
		Exec: &kubeconfigExec{APIVersion: execAPIVersion, Command: execCommand, Args: execArgs},
	})
	return yaml.Marshal(doc)
}

// renderTokenFileKubeconfig builds the tokenFile-authenticated kubeconfig
// -- the AuthKindOIDC rung's own renderer (this file's own top doc
// comment's "Design correction" section): tokenFilePath is where
// applyClusterBinding already wrote (and runCloudIdentityRefreshLoop
// keeps refreshing) this rung's own minted token -- client-go re-reads it
// directly off disk (verified against client-go's own source, same doc
// comment), so no exec plugin, no subcommand, no new IPC surface is
// needed at all.
func renderTokenFileKubeconfig(name, serverURL, caBundle, tokenFilePath string) ([]byte, error) {
	doc := buildKubeconfigDoc(name, serverURL, caBundle, kubeconfigUser{TokenFile: tokenFilePath})
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
// The AuthKindOIDC rung (only) also mints a token: ctx/client/sessionID/
// sandboxToken/gen/timeouts exist ENTIRELY for that one branch (mirrors
// applyCloudIdentityBinding's own identical parameter shape,
// cloudidentity.go) -- AuthKindCloud/AuthKindStatic never touch them. On
// success, the returned state is non-nil and must be appended to the
// SAME []cloudIdentityBindingState slice runCloudIdentityRefreshLoop
// already refreshes for every §27.3 cloud_identity_bindings token (run(),
// main.go) -- there is no separate refresh mechanism for this rung's own
// token; skipping that append would silently strand this token to expire
// unrefreshed. state is nil for AuthKindCloud/AuthKindStatic (nothing of
// this function's own minting to refresh -- AuthKindCloud's tokens are
// §27.3's, already refreshed via THAT mechanism; AuthKindStatic mints
// nothing at all) and nil on ANY failure, including a failure AFTER a
// successful mint (the orphaned token file is harmless -- resetCloudIdentityDir
// wipes it clean at the start of the very next boot regardless).
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
func applyClusterBinding(ctx context.Context, client credentials.CloudIdentityTokenMinter, sessionID, sandboxToken string, gen int, cluster *credentials.CloudIdentityConfigCluster, resolvedSandboxSecrets map[string]string, timeouts platform.Timeouts, dir string) (env []string, state *cloudIdentityBindingState) {
	if cluster == nil {
		return nil, nil
	}
	kubeconfigPath := filepath.Join(dir, kubeconfigFileName)

	authKind := clusterbinding.AuthKind(cluster.AuthKind)
	if !clusterbinding.IsValidAuthKind(authKind) {
		slog.Warn("sandbox-agent: cluster binding has an unrecognized auth kind, skipping kubeconfig injection", "auth_kind", cluster.AuthKind)
		return nil, nil
	}
	if err := clusterbinding.ValidateParams(authKind, cluster.Params); err != nil {
		slog.Warn("sandbox-agent: cluster binding has invalid params, skipping kubeconfig injection", "auth_kind", cluster.AuthKind, "error", err)
		return nil, nil
	}

	var doc []byte
	var mintedState *cloudIdentityBindingState
	switch authKind {
	case clusterbinding.AuthKindStatic:
		var p clusterbinding.StaticParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above
		value, ok := resolvedSandboxSecrets[p.SecretName]
		if !ok {
			slog.Warn("sandbox-agent: static cluster binding references a sandbox secret that did not resolve for this session, skipping kubeconfig injection", "secret_name", p.SecretName)
			return nil, nil
		}
		doc = []byte(value)

	case clusterbinding.AuthKindCloud:
		if cluster.ServerURL == nil || cluster.CaBundle == nil {
			slog.Warn("sandbox-agent: cloud-rung cluster binding missing serverUrl/caBundle, skipping kubeconfig injection")
			return nil, nil
		}
		var p clusterbinding.CloudParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above
		command, args := cloudExecCommand(p, cluster.Name)
		rendered, err := renderExecKubeconfig(cluster.Name, *cluster.ServerURL, *cluster.CaBundle, command, args)
		if err != nil {
			slog.Warn("sandbox-agent: render cloud-rung kubeconfig failed, skipping kubeconfig injection", "error", err)
			return nil, nil
		}
		doc = rendered

	case clusterbinding.AuthKindOIDC:
		if cluster.ServerURL == nil || cluster.CaBundle == nil {
			slog.Warn("sandbox-agent: oidc-rung cluster binding missing serverUrl/caBundle, skipping kubeconfig injection")
			return nil, nil
		}
		var p clusterbinding.OIDCParams
		_ = json.Unmarshal(cluster.Params, &p) // already validated above

		// Mint THIS rung's own token via the SAME mintCloudIdentityToken
		// every §27.3 cloud_identity_bindings caller uses (cloudidentity.go)
		// -- `aud` = the cluster's own configured client id
		// (clusterbinding.OIDCParams.ClientID), exactly what
		// kube-apiserver's own --oidc-client-id trusts. See this file's own
		// top doc comment's "Design correction" section for why this
		// replaces the original exec-plugin design.
		minted, mintOK := mintCloudIdentityToken(ctx, client, sessionID, sandboxToken, gen, p.ClientID, timeouts)
		if !mintOK {
			slog.Warn("sandbox-agent: mint oidc-rung cluster binding token failed, skipping kubeconfig injection", "client_id", p.ClientID)
			return nil, nil
		}
		tokenPath := filepath.Join(dir, oidcTokenFileName)
		if err := writeTokenFile(tokenPath, minted.Token); err != nil {
			slog.Warn("sandbox-agent: write oidc-rung cluster binding token file failed, skipping kubeconfig injection", "error", err)
			return nil, nil
		}
		rendered, err := renderTokenFileKubeconfig(cluster.Name, *cluster.ServerURL, *cluster.CaBundle, tokenPath)
		if err != nil {
			slog.Warn("sandbox-agent: render oidc-rung kubeconfig failed, skipping kubeconfig injection", "error", err)
			return nil, nil
		}
		doc = rendered
		// "cluster-oidc" (never one of cloudidentity.Kind's own aws/gcp/
		// azure/generic literals) -- this state describes a DIFFERENT
		// table's row (cluster_bindings, not cloud_identity_bindings);
		// refreshOneBinding only ever uses Kind for logging, never branches
		// on it, so a distinct, self-describing literal here costs nothing
		// and avoids implying this is a cloud_identity_bindings entry.
		mintedState = &cloudIdentityBindingState{Kind: "cluster-oidc", Audience: p.ClientID, TokenPath: tokenPath}
	}

	if err := writeTokenFile(kubeconfigPath, string(doc)); err != nil {
		slog.Warn("sandbox-agent: write kubeconfig failed, skipping kubeconfig injection", "path", kubeconfigPath, "error", err)
		return nil, nil
	}
	slog.Info("sandbox-agent: wrote kubeconfig", "auth_kind", cluster.AuthKind, "path", kubeconfigPath)
	return []string{clusterbinding.EnvVarKubeconfig + "=" + kubeconfigPath}, mintedState
}
