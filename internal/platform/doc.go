// Package platform holds cross-cutting infrastructure. Typed config
// (config.go) and the single timeout hierarchy (timeouts.go) landed in
// PR-02 (§5.4): Config is validated fail-fast at boot with named error
// types, and Timeouts carries both invariant chains plus their margin
// check. PR-03 (§5.3) added the structured-logging/OTel envelope:
// correlation-id context helpers + minting middleware (correlation.go),
// the slog JSON envelope (logging.go, plus Config.LogLevel), and the OTel
// SDK bootstrap (otel.go). PR-06 (§5.2) added the single HMAC auth helper
// (hmacauth.go). (§13.1) added the ws-token/user-session
// hash/mint helpers (tokenhash.go); §13.1 ("auth v1", §13.1) later
// added this package's remaining auth-adjacent primitives: the
// request-scoped AuthenticatedUser context helper (authcontext.go), the
// backend-issued user-session Set-Cookie construction (authcookie.go),
// and the AES-256-GCM provider-token encrypt/decrypt helpers
// (tokenencrypt.go).
package platform
