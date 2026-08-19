// This file tests runKubeCredentialHelper's own testable core
// (runKubeCredential) and its ExecCredential JSON encoding
// (buildExecCredentialJSON) -- see main.go's own doc comment on
// runKubeCredentialHelper for the full "exact git-credential-helper
// subcommand precedent" this mirrors.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

func TestBuildExecCredentialJSON(t *testing.T) {
	expiresAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	out, err := buildExecCredentialJSON(credentials.MintedCloudIdentityToken{Token: "the-jwt", ExpiresAt: expiresAt})
	if err != nil {
		t.Fatalf("buildExecCredentialJSON() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["apiVersion"] != execAPIVersion {
		t.Errorf("apiVersion = %v, want %q", parsed["apiVersion"], execAPIVersion)
	}
	if parsed["kind"] != "ExecCredential" {
		t.Errorf("kind = %v, want ExecCredential", parsed["kind"])
	}
	status, _ := parsed["status"].(map[string]any)
	if status["token"] != "the-jwt" {
		t.Errorf("status.token = %v, want %q", status["token"], "the-jwt")
	}
	if status["expirationTimestamp"] != "2026-01-01T12:00:00Z" {
		t.Errorf("status.expirationTimestamp = %v, want RFC3339 %q", status["expirationTimestamp"], "2026-01-01T12:00:00Z")
	}
}

func TestRunKubeCredential_Success_WritesExecCredentialToStdout(t *testing.T) {
	m := &fakeMinter{tokens: map[string]credentials.MintedCloudIdentityToken{
		"my-client-id": {Token: "minted-jwt", ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}}
	var stdout bytes.Buffer

	err := runKubeCredential(context.Background(), &stdout, m, "sess-1", "sandbox-token", 1, "my-client-id", fastMintTimeouts())
	if err != nil {
		t.Fatalf("runKubeCredential() error = %v", err)
	}

	var parsed map[string]any
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &parsed); unmarshalErr != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout = %q)", unmarshalErr, stdout.String())
	}
	status, _ := parsed["status"].(map[string]any)
	if status["token"] != "minted-jwt" {
		t.Errorf("status.token = %v, want %q", status["token"], "minted-jwt")
	}
}

func TestRunKubeCredential_MintFailure_ReturnsErrorNeverWritesStdout(t *testing.T) {
	m := &fakeMinter{failFirstN: 100, statusCode: http.StatusForbidden}
	var stdout bytes.Buffer

	err := runKubeCredential(context.Background(), &stdout, m, "sess-1", "sandbox-token", 1, "my-client-id", fastMintTimeouts())
	if err == nil {
		t.Fatal("runKubeCredential() error = nil, want an error")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty -- a failed mint must never emit a (partial/empty-token) ExecCredential document", stdout.String())
	}
}

func TestRunKubeCredentialHelper_UsageError(t *testing.T) {
	tests := [][]string{nil, {}, {"a", "b"}}
	for _, args := range tests {
		if err := runKubeCredentialHelper(args); err == nil {
			t.Errorf("runKubeCredentialHelper(%v) error = nil, want a usage error", args)
		}
	}
}
