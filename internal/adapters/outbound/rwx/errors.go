package rwx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// --- Config errors (New) -- named, structured, fail-fast, matching the
// established platform/config.go / modal.MissingConfigError pattern. ---

// MissingConfigError is returned by New when a required Config field
// (CLIPath, AccessToken) is empty.
type MissingConfigError struct {
	Field string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf("rwx: missing required Config.%s", e.Field)
}

// --- Unsupported-operation errors -- the permanent ProviderError every
// method Capabilities() reports as unsupported returns, mirroring
// modal.Provider.ResumeSandbox's own established pattern (modal/errors.go's
// errResumeUnsupported) for an operation a provider's own Capabilities()
// already reports as unsupported. ---

var (
	// errResumeUnsupported backs ResumeSandbox: Capabilities().Resume is
	// false (see Provider.Capabilities' own doc comment for the full
	// "settled empirically" reasoning, §4.1.3).
	errResumeUnsupported = errors.New("rwx: ResumeSandbox is not supported; Capabilities().Resume is false")
	// errSnapshotsUnsupported backs TakeSnapshot/RestoreFromSnapshot:
	// Capabilities().Snapshots is false (§4.1.1: RWX's own content-addressed
	// cache is not an addressable take-snapshot-now/restore-from-handle
	// API).
	errSnapshotsUnsupported = errors.New("rwx: snapshot operations are not supported; Capabilities().Snapshots is false")
	// errImageBuildsUnsupported backs BuildImage/DeleteImage:
	// Capabilities().ImageBuilds is false (§4.1.1: `rwx image build|push|pull`
	// exist, but no image DELETE is documented, and ImageBuilds covers both
	// halves of the pair).
	errImageBuildsUnsupported = errors.New("rwx: image-build operations are not supported; Capabilities().ImageBuilds is false")
)

// --- CLI-path classification (sandbox lifecycle: start/stop/list) ---
//
// §4.1.1: "on the CLI path, the classification inputs are the process
// exit code and the --format json error envelope -- NEVER prose message
// matching." §4.1.3: "no published error-code table for the CLI... beyond
// {status: 'error', error: string}; exit-code/envelope classification is
// pinned by contract tests, unknown -> transient." This package has no
// real pinned binary to observe a real exit-code table from (the
// deliberate, named scope gap this Step's own PR describes) -- so, unlike
// modal's HTTP-status-class table (which at least has a real, if invented,
// per-status verdict for every code in the table), EVERY nonzero exit
// classifies transient today. The table below is the one place a future
// real-binary-verified exit code gets its own permanent-vs-transient
// verdict once contract tests pin it -- adding a case here is the whole
// migration, no caller-side change needed.

// cliErrorEnvelope is the `--format json` error shape RWX's own docs name
// (§4.1.3). Decoded best-effort, purely to populate a human-readable
// message for logging/debugging -- classification itself (see
// classifyCLIError) never depends on whether this decodes successfully.
type cliErrorEnvelope struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// classifyCLIError builds the ProviderError for a non-zero `rwx` CLI exit.
// exitCode -1 means the process never completed at all (spawn failure, or
// killed by this call's own context deadline, runner.go) -- always
// transient, mirroring modal.classifyNetworkError's identical "a transport/
// process-level hiccup is exactly what retry-with-backoff exists for"
// reasoning.
//
// stdout/stderr are NEVER embedded verbatim in the returned error, only
// envelope.Error (a decoded, NAMED field) or a fixed fallback string --
// mirroring modal.classifyErrorResponse's own identical discipline
// (SESSION_CONFIG, §6.4, carries a plaintext sandbox bearer token, §5.2;
// ProviderError.Error() is logged on every transition, §5.3; a CLI that
// echoes its own invalid input back on a validation failure is exactly
// the kind of output that could otherwise leak that token into a log
// line).
func classifyCLIError(op ports.Op, exitCode int, stdout, stderr []byte, processErr error) *ports.ProviderError {
	if exitCode == -1 {
		return &ports.ProviderError{
			Transient: true,
			Code:      "PROCESS_ERROR",
			Op:        op,
			Err:       fmt.Errorf("rwx: cli: process did not complete (spawn failure or exec timeout): %w", processErr),
		}
	}

	message := "no error output"
	var envelope cliErrorEnvelope
	if err := json.Unmarshal(stdout, &envelope); err == nil && envelope.Error != "" {
		message = envelope.Error
	} else if len(stderr) > 0 {
		message = "see subprocess stderr (omitted from this error to avoid leaking echoed request content, e.g. SESSION_CONFIG)"
	}

	return &ports.ProviderError{
		// §4.1.3: "unknown -> transient" -- no real exit code has been
		// pinned permanent yet; see this file's own top comment.
		Transient: true,
		Code:      fmt.Sprintf("exit_%d", exitCode),
		Op:        op,
		Err:       fmt.Errorf("rwx: cli: exit %d: %s", exitCode, message),
	}
}

// --- Dispatches-API-path classification (preview dispatch only, §4.1.2;
// never sandbox lifecycle) ---
//
// ports.Notifier.Deliver's own contract (ports/notifier.go) is that a
// delivery failure is NEVER classified transient/permanent by this port
// -- outboxworker's own domain/outbox.EvaluateBackoff decision is driven
// purely by attempt count, not by inspecting the error's own shape (the
// SAME "no typed classification" precedent ports.SourceControl's plain
// errors already establish). DispatchError below is therefore NOT a
// ports.ProviderError (that type's own Op is scoped to SandboxProvider
// methods, which the Dispatches API is never one of) -- it is this
// package's OWN plain, structured error, computing and exposing the SAME
// HTTP-status-class verdict §4.1.1 describes (mirroring modal's table
// exactly: network failure/429/5xx transient; 400/401/403/404/409/422
// permanent; anything else transient, §3.2's "unknown defaults to
// transient" rule) for logging/observability -- Transient is never
// consulted by any caller in this codebase today, but is real, tested
// logic, not a placeholder.

// isTransientDispatchStatus classifies an HTTP response status code from
// the Dispatches API, mirroring modal.isTransientStatus's table exactly
// (§3.2's own rule applies identically to every SandboxProvider/outbound
// adapter in this codebase, not just Modal's).
func isTransientDispatchStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return false
	}
	if status >= http.StatusInternalServerError {
		return true
	}
	// Anything else -- a status this table has no explicit entry for --
	// defaults to transient, never permanent (§3.2).
	return true
}

// dispatchErrorBody is the error envelope RWX's own docs name for the
// Dispatches API (§4.1.3: "{status: 'error', error: string}").
type dispatchErrorBody struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// DispatchError is the error DispatchClient returns for any non-2xx RWX
// Dispatches API response -- see this section's own top comment for why
// this is a plain, package-local type rather than ports.ProviderError.
type DispatchError struct {
	Status    int
	Message   string
	Transient bool
}

func (e *DispatchError) Error() string {
	class := "permanent"
	if e.Transient {
		class = "transient"
	}
	return fmt.Sprintf("rwx: dispatches api: %s: http %d: %s", class, e.Status, e.Message)
}

// classifyDispatchErrorResponse builds a DispatchError for a non-2xx
// Dispatches API response. Mirrors modal.classifyErrorResponse's own
// "decode best-effort for a human message, but the response body is NEVER
// embedded verbatim" discipline -- a dispatch request body carries the
// pushed sha/PR number/session id (ports.Notification's own payload,
// PreviewDispatchPayload below), not a bearer secret, but the same
// never-echo-raw-bytes posture costs nothing to keep uniform across every
// outbound adapter in this codebase.
func classifyDispatchErrorResponse(status int, body []byte) *DispatchError {
	message := "no error body"
	var parsed dispatchErrorBody
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != "" {
		message = parsed.Error
	} else if len(body) > 0 {
		message = "error body did not match the expected error envelope"
	}
	return &DispatchError{Status: status, Message: message, Transient: isTransientDispatchStatus(status)}
}

// classifyDispatchNetworkError builds a DispatchError for a request that
// never got an HTTP response at all -- mirrors modal.classifyNetworkError
// exactly (§4.1's table treats every network-level failure as transient).
func classifyDispatchNetworkError(err error) *DispatchError {
	status := 0
	message := "network error: " + errorClass(err)
	return &DispatchError{Status: status, Message: message, Transient: true}
}

// errorClass distinguishes a timeout from every other transport failure
// for logging purposes only (mirrors modal.classifyNetworkError's
// identical net.Error.Timeout() check) -- it never changes the Transient
// verdict, which is always true for a network-level failure.
func errorClass(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "transport failure"
}
