package modal

import "github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"

// This file holds the request/response JSON shapes this adapter sends to
// and expects back from Modal's API. As doc.go explains, there is no real
// Modal API reachable from this codebase — these shapes are this
// adapter's own invention, pinned by the tests in provider_test.go against
// a fake httptest.Server, not against real Modal API docs.
//
// The one hard requirement every shape below honors is §4.1's "sandbox
// env passed as one SESSION_CONFIG JSON document — the provider never
// assembles env fragments": every request that needs SESSION_CONFIG
// carries it as a single nested sessionConfig object, never spread across
// top-level fields.

// createSandboxRequest is the body POSTed to /v1/sandboxes.
type createSandboxRequest struct {
	Gen           int                         `json:"gen"`
	Image         string                      `json:"image,omitempty"`
	SessionConfig sessionconfig.SessionConfig `json:"sessionConfig"`
}

// restoreSandboxRequest is the body POSTed to /v1/sandboxes/restore.
type restoreSandboxRequest struct {
	SnapshotID    string                      `json:"snapshotId"`
	Gen           int                         `json:"gen"`
	Image         string                      `json:"image,omitempty"`
	SessionConfig sessionconfig.SessionConfig `json:"sessionConfig"`
}

// imageBuildRequest is the body POSTed to /v1/images.
type imageBuildRequest struct {
	Base           string            `json:"base"`
	RepoSHAs       map[string]string `json:"repoShas,omitempty"`
	RuntimeVersion string            `json:"runtimeVersion,omitempty"`
}

// sandboxResponse is returned by CreateSandbox and RestoreFromSnapshot on
// success.
type sandboxResponse struct {
	SandboxID string `json:"sandboxId"`
}

// snapshotResponse is returned by TakeSnapshot on success.
type snapshotResponse struct {
	SnapshotID string `json:"snapshotId"`
}

// buildResponse is returned by BuildImage on success.
type buildResponse struct {
	BuildID string `json:"buildId"`
}

// listResponse is returned by List on success.
type listResponse struct {
	Sandboxes []sandboxResponse `json:"sandboxes"`
}

// modalErrorBody is the error envelope this adapter expects Modal to
// return on non-2xx responses. Error.Code, when present, becomes
// ports.ProviderError.Code (for logging/debugging only — see errors.go:
// the transient/permanent CLASSIFICATION itself is driven by HTTP status,
// never by this code or by Error.Message).
type modalErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
