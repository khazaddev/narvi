// Package platform will hold cross-cutting infrastructure: typed config
// validated at boot, the single timeout hierarchy (platform/timeouts.go),
// the structured-logging/OTel envelope, and the HMAC auth helper —
// implemented across PR-02 (§5.4), PR-03 (§5.3), and PR-06 (§5.2).
package platform
