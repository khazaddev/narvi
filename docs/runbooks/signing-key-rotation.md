# Cloud-identity OIDC signing-key rotation

Step 73a ("cloud identity: OIDC federation + kubeconfig", §27.3). This is
a routine admin procedure, not a failure mode — included here because §32.9
already sets the precedent of documenting an operator procedure alongside
the failure runbooks, and this is the other "an admin needs to know how to
do this on purpose" surface Steps 73a/74/76 added. No alert backs it: there
is nothing to alert on until a rotation is overdue, and this codebase
deliberately ships NO scheduled-rotation trigger (see below).

## What triggers rotation

**Manual only, by design** — `internal/domain/oidckey/doc.go`'s own top
comment states this directly: §27.3 specifies rotation's SHAPE (overlap
window >= max token lifetime) but the plan's own §27.8 explicitly defers
automatic scheduled rotation "until operational experience says what
cadence is right." There is no age-threshold check, no cron job, no
timer — rotation happens only when an admin calls the endpoint below.

## Procedure

1. **Authorization**: `POST /api/cloud-identity/signing-keys/rotate`,
   gated by `authz.ActionManageCloudIdentityKeys` — admin role only (the
   same RBAC row integrations/global-secrets management already occupies,
   since this is a platform-wide security-posture change, not scoped to
   one Environment).
2. **What happens**: `postgres.OIDCSigningKeyStore.Rotate`
   (`internal/adapters/outbound/postgres/oidcsigningkey_store.go`)
   atomically, in one transaction: retires the current active key
   (`retired_at = now`) and creates a brand-new one as the new active
   signing key. There is never a moment with zero or two active keys once
   at least one rotation has run.
3. **Overlap window**: the just-retired key stays PUBLISHABLE in the JWKS
   response (`GET /.well-known/openid-configuration` + JWKS endpoint,
   `internal/adapters/inbound/httpapi/oidcdiscovery.go`) — and therefore
   still able to verify a token signed under it — for
   `platform.Timeouts.CloudIdentitySigningKeyOverlapWindow` (15 minutes)
   after retirement. This is comfortably above
   `CloudIdentityTokenLifetime` (10 minutes, §27.3's own "exp ~= 10 min")
   with the same explicit margin `timeouts.Validate()` enforces elsewhere
   in this file — a token minted moments before rotation is guaranteed to
   still verify for its own full lifetime.
4. **Confirm it worked**: the rotation endpoint's own response
   (`RotateCloudIdentitySigningKeyResponse`) reports what was retired vs.
   created; an `audit_log` row (`cloud_identity_signing_key.rotated`,
   keyed by the new key's own `kid`) is written in the SAME transaction —
   query `audit_log` for that action to confirm a rotation actually
   happened and by whom, independent of trusting the HTTP response alone.

## When to rotate (operator judgment, not automated)

- Suspected private-key compromise (the strongest reason — rotate
  immediately, then confirm no unexpected mints occurred via
  `cloud_identity_mint_total`, tagged `kind`, for the affected window).
- Routine security hygiene on whatever cadence the organization's own
  policy requires (no built-in reminder — track this externally).

## Failure mode: minting breaks because no active key exists

Before the FIRST rotation ever runs (a fresh deployment), there is no
active signing key at all — `cloud-identity-token` minting fails closed
with `"no active signing key configured"` (log:
`"httpapi: cloud-identity-token: get active signing key failed"`,
`internal/adapters/inbound/httpapi/cloudidentitytoken.go`), and the JWKS
endpoint returns a valid but empty `{"keys": []}` document rather than
crashing. **Remediation**: run the rotation procedure above once — the
first rotation on an empty store creates the first active key with no
previous one to retire; `RotateCloudIdentitySigningKeyResponse`'s own
`RetiredKid`/`RetiredAt`/`PublishableUntil` fields are left `nil` for
exactly this case (`internal/adapters/inbound/httpapi/
cloudidentitykeys.go`), distinguishing a genuine first-ever rotation from
an ordinary one with a real key retired.

## Resilience scenario

No §9.3-catalogued scenario covers this — it is a deliberate operator
action, not a failure this system recovers from on its own. The
rotation mechanism itself (atomicity, overlap-window publishability, the
empty-store first-rotation case) is covered by
`internal/domain/oidckey`'s own unit tests and
`internal/adapters/outbound/postgres`'s own `oidcsigningkey_store`
integration tests, not a resilience scenario.
