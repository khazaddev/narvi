package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

func strPtrKC(s string) *string { return &s }

func TestApplyClusterBinding_Nil(t *testing.T) {
	dir := t.TempDir()
	env := applyClusterBinding(nil, nil, dir)
	if env != nil {
		t.Errorf("applyClusterBinding(nil, ...) = %v, want nil", env)
	}
	if _, err := os.Stat(filepath.Join(dir, kubeconfigFileName)); err == nil {
		t.Errorf("kubeconfig file must not be written when cluster is nil")
	}
}

func TestApplyClusterBinding_Static(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"secretName": "KUBE_STATIC_CONFIG"})
	cluster := &credentials.CloudIdentityConfigCluster{
		Name: "prod", AuthKind: "static", Params: params,
	}
	secrets := map[string]string{"KUBE_STATIC_CONFIG": "apiVersion: v1\nkind: Config\n# a real uploaded kubeconfig, verbatim\n"}

	env := applyClusterBinding(cluster, secrets, dir)
	wantPath := filepath.Join(dir, kubeconfigFileName)
	if len(env) != 1 || env[0] != "KUBECONFIG="+wantPath {
		t.Fatalf("env = %v, want [KUBECONFIG=%s]", env, wantPath)
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	if string(got) != secrets["KUBE_STATIC_CONFIG"] {
		t.Errorf("kubeconfig content = %q, want the sandbox secret's own value VERBATIM (%q) -- §27.4: never env-var-expanded", got, secrets["KUBE_STATIC_CONFIG"])
	}
}

func TestApplyClusterBinding_StaticMissingSecret(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"secretName": "KUBE_STATIC_CONFIG"})
	cluster := &credentials.CloudIdentityConfigCluster{Name: "prod", AuthKind: "static", Params: params}

	env := applyClusterBinding(cluster, map[string]string{}, dir)
	if env != nil {
		t.Errorf("applyClusterBinding() = %v, want nil (referenced secret did not resolve)", env)
	}
}

func TestApplyClusterBinding_Cloud(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"cloud": "aws", "region": "us-east-1"})
	cluster := &credentials.CloudIdentityConfigCluster{
		Name: "prod-eks", AuthKind: "cloud", Params: params,
		ServerURL: strPtrKC("https://ABCDEF.gr7.us-east-1.eks.amazonaws.com"),
		CaBundle:  strPtrKC("-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n"),
	}

	env := applyClusterBinding(cluster, nil, dir)
	wantPath := filepath.Join(dir, kubeconfigFileName)
	if len(env) != 1 || env[0] != "KUBECONFIG="+wantPath {
		t.Fatalf("env = %v, want [KUBECONFIG=%s]", env, wantPath)
	}

	doc := readKubeconfigDoc(t, wantPath)
	if len(doc.Clusters) != 1 || doc.Clusters[0].Cluster.Server != *cluster.ServerURL {
		t.Errorf("clusters = %+v, want server %q", doc.Clusters, *cluster.ServerURL)
	}
	wantCA := base64.StdEncoding.EncodeToString([]byte(*cluster.CaBundle))
	if doc.Clusters[0].Cluster.CertificateAuthorityData != wantCA {
		t.Errorf("certificate-authority-data = %q, want base64(caBundle) = %q", doc.Clusters[0].Cluster.CertificateAuthorityData, wantCA)
	}
	if len(doc.Users) != 1 {
		t.Fatalf("users = %+v, want exactly 1", doc.Users)
	}
	exec := doc.Users[0].User.Exec
	if exec.Command != "aws" {
		t.Errorf("exec.command = %q, want %q", exec.Command, "aws")
	}
	wantArgs := []string{"eks", "get-token", "--cluster-name", "prod-eks", "--region", "us-east-1"}
	if strings.Join(exec.Args, ",") != strings.Join(wantArgs, ",") {
		t.Errorf("exec.args = %v, want %v", exec.Args, wantArgs)
	}
}

func TestApplyClusterBinding_CloudGCPNoRegionArg(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"cloud": "gcp"})
	cluster := &credentials.CloudIdentityConfigCluster{
		Name: "prod-gke", AuthKind: "cloud", Params: params,
		ServerURL: strPtrKC("https://gke.example"),
		CaBundle:  strPtrKC("ca-bundle"),
	}
	env := applyClusterBinding(cluster, nil, dir)
	if len(env) != 1 {
		t.Fatalf("env = %v, want 1 entry", env)
	}
	doc := readKubeconfigDoc(t, filepath.Join(dir, kubeconfigFileName))
	exec := doc.Users[0].User.Exec
	if exec.Command != "gke-gcloud-auth-plugin" {
		t.Errorf("exec.command = %q, want %q", exec.Command, "gke-gcloud-auth-plugin")
	}
	if len(exec.Args) != 0 {
		t.Errorf("exec.args = %v, want empty", exec.Args)
	}
}

func TestApplyClusterBinding_CloudMissingServerURL(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"cloud": "aws"})
	cluster := &credentials.CloudIdentityConfigCluster{Name: "prod", AuthKind: "cloud", Params: params, CaBundle: strPtrKC("ca")}
	env := applyClusterBinding(cluster, nil, dir)
	if env != nil {
		t.Errorf("applyClusterBinding() = %v, want nil (missing serverUrl)", env)
	}
}

func TestApplyClusterBinding_OIDC(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"clientId": "my-oidc-client"})
	cluster := &credentials.CloudIdentityConfigCluster{
		Name: "self-managed", AuthKind: "oidc", Params: params,
		ServerURL: strPtrKC("https://k8s.example:6443"),
		CaBundle:  strPtrKC("ca-bundle"),
	}

	env := applyClusterBinding(cluster, nil, dir)
	if len(env) != 1 {
		t.Fatalf("env = %v, want 1 entry", env)
	}
	doc := readKubeconfigDoc(t, filepath.Join(dir, kubeconfigFileName))
	exec := doc.Users[0].User.Exec
	if exec.APIVersion != execAPIVersion {
		t.Errorf("exec.apiVersion = %q, want %q", exec.APIVersion, execAPIVersion)
	}
	selfPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if exec.Command != selfPath {
		t.Errorf("exec.command = %q, want this binary's own path %q", exec.Command, selfPath)
	}
	wantArgs := []string{"kube-credential", "my-oidc-client"}
	if strings.Join(exec.Args, ",") != strings.Join(wantArgs, ",") {
		t.Errorf("exec.args = %v, want %v", exec.Args, wantArgs)
	}
}

func TestApplyClusterBinding_UnrecognizedAuthKind(t *testing.T) {
	dir := t.TempDir()
	cluster := &credentials.CloudIdentityConfigCluster{Name: "x", AuthKind: "manual", Params: json.RawMessage(`{}`)}
	env := applyClusterBinding(cluster, nil, dir)
	if env != nil {
		t.Errorf("applyClusterBinding() = %v, want nil", env)
	}
}

func TestApplyClusterBinding_InvalidParams(t *testing.T) {
	dir := t.TempDir()
	cluster := &credentials.CloudIdentityConfigCluster{
		Name: "x", AuthKind: "cloud", Params: json.RawMessage(`{}`), // missing "cloud" field
		ServerURL: strPtrKC("https://x"), CaBundle: strPtrKC("ca"),
	}
	env := applyClusterBinding(cluster, nil, dir)
	if env != nil {
		t.Errorf("applyClusterBinding() = %v, want nil", env)
	}
}

// --- kubeconfig file permissions ---

func TestApplyClusterBinding_KubeconfigPermissions(t *testing.T) {
	dir := t.TempDir()
	params, _ := json.Marshal(map[string]string{"secretName": "S"})
	cluster := &credentials.CloudIdentityConfigCluster{Name: "x", AuthKind: "static", Params: params}
	applyClusterBinding(cluster, map[string]string{"S": "kubeconfig content"}, dir)

	fi, err := os.Stat(filepath.Join(dir, kubeconfigFileName))
	if err != nil {
		t.Fatalf("stat kubeconfig: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("kubeconfig file mode = %o, want 0600", perm)
	}
}

// --- test helpers ---

func readKubeconfigDoc(t *testing.T, path string) kubeconfigDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	var doc kubeconfigDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("kubeconfig is not valid YAML: %v", err)
	}
	return doc
}
