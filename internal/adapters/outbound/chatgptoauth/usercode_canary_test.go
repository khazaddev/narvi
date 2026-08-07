//go:build canary

// This file implements §29.7's own named scheduled canary: "the
// unauthenticated deviceauth/usercode start call is a good *scheduled*
// canary for §29.2-shape drift, never a PR gate". Gated behind the
// "canary" build tag specifically so it NEVER runs as part of `go test
// ./...` or `go test -tags=integration ./...` -- a genuine, unmocked
// network call to a real, third-party production service
// (auth.openai.com) has no place on the per-PR path (this package's own
// doc.go: "a third-party production service has no place on the per-PR
// path"), but the call itself needs no OpenAI credential at all (the
// device flow's own first step is, by design, unauthenticated -- a
// client only needs its own public client_id) and returns a real,
// observable device code/interval whose SHAPE is exactly what this
// package's own StartDeviceAuth already asserts.
//
// Wiring an actual schedule (a cron-triggered CI job running `go test
// -tags=canary ./internal/adapters/outbound/chatgptoauth/...`) is
// deliberately NOT done here: this Step's own scope explicitly excludes
// touching .github/. This file is the honest, runnable HALF of that
// canary -- real logic, real assertions, real network call -- left for
// whoever wires the schedule to point at, exactly as approved for this
// Step's own "structure it honestly... do NOT fake a pass" instruction.
package chatgptoauth

import (
	"net/http"
	"testing"
	"time"
)

// TestUsercodeCanary_RealAuthOpenAICall makes ONE real, unauthenticated
// POST to auth.openai.com's own deviceauth/usercode endpoint via this
// package's own StartDeviceAuth (DefaultBaseURL, not a fake server) and
// asserts the response still matches §29.2's own verified shape
// (device_auth_id/user_code/interval, all non-empty/non-zero). A failure
// here means either this environment has no outbound internet access to
// auth.openai.com, or (the case this canary actually exists to catch)
// OpenAI has changed this endpoint's own shape since §29.2's own research
// (client-id allowlisting per originator, a renamed field, a dropped
// endpoint -- §29.10 risk 1's own named, expected drift modes).
func TestUsercodeCanary_RealAuthOpenAICall(t *testing.T) {
	c := New(http.DefaultClient, DefaultBaseURL, 15*time.Second)

	got, err := c.StartDeviceAuth(t.Context())
	if err != nil {
		t.Fatalf("StartDeviceAuth() against the REAL auth.openai.com error = %v -- either no outbound network access from this environment, or §29.2's own verified deviceauth/usercode shape has drifted (§29.10 risk 1)", err)
	}
	if got.DeviceAuthID == "" {
		t.Error("DeviceAuthID = empty, want a real value")
	}
	if got.UserCode == "" {
		t.Error("UserCode = empty, want a real value")
	}
	if got.Interval <= 0 {
		t.Errorf("Interval = %v, want > 0", got.Interval)
	}
	t.Logf("usercode canary: device_auth_id=%q user_code=%q interval=%v -- real, unauthenticated auth.openai.com response, unconsumed (never polled)", got.DeviceAuthID, got.UserCode, got.Interval)
}
