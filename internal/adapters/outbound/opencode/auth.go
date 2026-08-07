package opencode

import (
	"context"
	"net/http"
	"net/url"
)

// authOAuthRequest is PUT /auth/{providerID}'s own "oauth" Auth-union
// member (§29.1/§29.6) — VERIFIED live against the pinned OpenCode
// 1.17.15 binary's own GET /doc OpenAPI schema during this Step's own
// research pass: components.schemas.OAuth requires exactly {"type",
// "refresh", "access", "expires"} (accountId/enterpriseUrl optional,
// additionalProperties: false). Refresh has NO `omitempty` — deliberate:
// it must always be sent as a real (if empty) string key, never omitted,
// matching what was independently verified live (PUT /auth/openai with
// {..., "refresh": ""} returns `true`).
type authOAuthRequest struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId,omitempty"`
}

// OAuthCredential is SetOAuthAuth's own exported parameter shape — plain
// data, with NO field for a refresh token at all. This is deliberate, not
// an oversight: it is this package's own structural enforcement of §29.6
// ("sandbox-agent injects refresh: \"\"") — the caller (cmd/sandbox-agent/
// main.go) constructs one of these from the CP delivery response, which
// itself never carries a refresh token either (see internal/adapters/
// inbound/httpapi/providercredentialsdelivery.go's own credentialAuthValue
// doc comment for the full chain) — so there is no field anywhere on this
// call path a bug could accidentally populate with a real refresh token.
type OAuthCredential struct {
	Access string
	// Expires is the access token's own absolute expiry, epoch
	// milliseconds — OpenCode's own "expires" field shape (§29.1: "expires
	// (epoch ms)"), passed through verbatim from the CP delivery
	// response's own "expires" field (already epoch-ms there too).
	Expires   int64
	AccountID string
}

// SetOAuthAuth implements §29.6's own sandbox-injection call: PUT
// /auth/{providerID} carrying the "oauth" Auth-union member, with
// Refresh HARDCODED to the empty string right here — never sourced from
// cred (which structurally cannot carry one, see OAuthCredential's own
// doc comment) or from anywhere else. Sequenced by the caller (cmd/
// sandbox-agent/main.go's own run()) strictly between opencodeproc.Spawn
// reporting healthy and the WS bridge accepting its first "prompt"
// command — §29.6: "sequenced inside the spawn/readiness path so a turn
// can never race an unauthenticated provider".
//
// On success, OpenCode's own response is a bare JSON boolean (§29.1:
// "returns true") — decoded into a throwaway *bool, mirroring
// forceCompaction's own identical precedent (compact.go); only whether
// the call errored at all matters to this method's own caller, which
// treats a failure as non-fatal to boot (logged + a wire Warning, §29.6 —
// never a boot failure, since the credential is delivered independently
// of whether this session's turns will ever name an openai/... model).
func (a *Adapter) SetOAuthAuth(ctx context.Context, providerID string, cred OAuthCredential) error {
	body := authOAuthRequest{
		Type:      "oauth",
		Access:    cred.Access,
		Refresh:   "",
		Expires:   cred.Expires,
		AccountID: cred.AccountID,
	}
	path := "/auth/" + url.PathEscape(providerID)
	var result bool
	return a.doJSON(ctx, http.MethodPut, path, body, &result)
}
