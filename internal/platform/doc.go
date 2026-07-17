// Package platform holds cross-cutting infrastructure. Typed config
// (config.go) and the single timeout hierarchy (timeouts.go) landed in
// PR-02 (§5.4): Config is validated fail-fast at boot with named error
// types, and Timeouts carries both invariant chains plus their margin
// check. PR-03 (§5.3) added the structured-logging/OTel envelope:
// correlation-id context helpers + minting middleware (correlation.go),
// the slog JSON envelope (logging.go, plus Config.LogLevel), and the OTel
// SDK bootstrap (otel.go). The HMAC auth helper (PR-06, §5.2) is still
// pending.
package platform
