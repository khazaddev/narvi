// Package credentials implements §5.2's git credential helper: "sandbox-
// agent implements a git credential helper that POSTs to CP
// /sessions/{id}/scm-credentials (sandbox bearer), caches to disk with
// flock, 5-min expiry buffer, scoped https+host only. Never fall back to
// stale cache." This is the highest-stakes surface in this Step --
// git itself invokes this exact protocol on every clone/fetch/push, so a
// protocol mistake here breaks every git operation, not just a test.
//
// The git-credential-helper protocol (descriptor.go): git invokes a
// configured helper as `<helper-command> <op>` where op is exactly one of
// get/store/erase. For all three, git writes a series of `key=value\n`
// lines to the helper's stdin, terminated by a blank line or EOF
// (ParseDescriptor). For `get` only, on success the helper writes
// `username=...\npassword=...\n` to stdout (nothing else) and exits 0; if
// it cannot/will not answer, it writes NOTHING to stdout and still exits 0
// -- NOT a nonzero exit -- git treats a helper producing no output as
// "this helper had nothing to add", not a hard failure. `store`/`erase`
// never write to stdout; they just read+consume stdin and exit 0.
//
// Disk cache (cache.go): one JSON file per host inside Config.
// CredentialCacheDir, filename derived by hashing the (lowercased) host
// with SHA-256 -- never the raw host string, so a malicious/unexpected
// host value can never escape the cache directory or cause a path-
// traversal write. Every read/write/erase is protected by an exclusive
// flock held on that exact file for the duration of the operation: two
// concurrent `git clone` processes hitting different repos on the same
// host could otherwise race on the same cache file. Directory/file
// permissions are 0700/0600 -- these are real secrets.
//
// "Never fall back to stale cache" (get.go, Get/freshCacheHit): enforced
// BY CONSTRUCTION, not by discipline. freshCacheHit is the ONLY place that
// ever reads a cached Credential; its own stale-or-miss branch discards
// the value (returns the zero Credential) before Get's caller ever sees
// it. Get itself holds no variable that could carry a stale Credential
// past that call -- the only Credential a Fetch-failure path can possibly
// return is the zero value alongside a non-nil error, never the discarded
// stale one.
//
// CP client (cpclient.go): CPClient.Fetch POSTs to CP's future
// /sessions/{id}/scm-credentials endpoint. THAT ENDPOINT DOES NOT EXIST
// YET -- §9.3's own "e2e happy path" work is
// what actually builds it. Exactly like §4.1 invented a documented,
// tested-against-a-fake-server wire contract for Modal (a real external
// API this codebase can't reach in CI), this file invents a plausible,
// documented request/response shape here and tests CPClient's CP-calling
// logic against a fake httptest.Server standing in for it -- whoever
// implements Step 21 reconciles the two sides then, an accepted, explicit
// future reconciliation point. NewCPClient derives its REST base URL from
// SessionConfig.ControlPlaneWsUrl (swap wss/ws for https/http, keep only
// the host) since SESSION_CONFIG has no separate REST base URL field and
// this Step does not add one to the schema.
//
// cmd/sandbox-agent/main.go's runCredentialHelper subcommand is the actual
// process entry point git invokes (via the `!'<this binary>'
// credential-helper` value internal/sandboxagent/gitclone configures on
// every clone) -- it is a SEPARATE process spawned by git, re-reading the
// same NARVI_* env vars sandbox-agent's own boot.Load() already defines
// (inherited, since supervisor.Spec.Env is nil in gitclone's Spawn call),
// not a duplication of that config-loading logic.
package credentials
