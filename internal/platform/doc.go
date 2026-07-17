// Package platform holds cross-cutting infrastructure. Typed config
// (config.go) and the single timeout hierarchy (timeouts.go) landed in
// PR-02 (§5.4): Config is validated fail-fast at boot with named error
// types, and Timeouts carries both invariant chains plus their margin
// check. The structured-logging/OTel envelope (PR-03, §5.3) and the HMAC
// auth helper (PR-06, §5.2) are still pending.
package platform
