-- cloud_identity_bindings: connects an Environment (or every Environment,
-- at global scope) to a customer cloud role/identity-provider config
-- (Step 73a, "cloud identity: OIDC issuer, bindings, minting", §27.3).
-- Shape matches §27.3 verbatim: "cloud_identity_bindings(scope
-- ENUM('environment','global'), scope_target_id, kind
-- ENUM('aws','gcp','azure','generic'), audience, params JSONB,
-- timestamps), at most one per (scope target, kind) in v1."
--
-- cloud_identity_binding_scope is DELIBERATELY NARROWER than provider_
-- credential_scope/sandbox_secret_scope -- only 2 of providercredential.
-- Scope's own recognized values (environment, global) apply here, per
-- §27.3's own explicit "Deliberately no repo scope: a deployment target
-- is an Environment property (§14.1's own model), not a repo property."
-- No automation/user scope either -- neither concept applies to a cloud
-- role grant. Resolution (environment shadows global, most specific
-- wins) still runs through internal/domain/providercredential's own
-- Scope/Resolve, exactly like sandbox_secrets does (that package's own
-- doc.go) -- reusing the SAME generic priority map is safe specifically
-- BECAUSE this table restricts itself to the 2 scope values whose
-- relative priority (environment=2 < global=4) that map already encodes
-- correctly; see internal/domain/cloudidentity's own doc comment for the
-- restriction enforced at the Go layer (a Candidate never gets built
-- with any OTHER Scope value for this table).
CREATE TYPE cloud_identity_binding_scope AS ENUM ('environment', 'global');

-- cloud_identity_binding_kind: the 4 recognized cloud/identity-consumer
-- families §27.3 names. 'generic' is not a placeholder -- it is the real
-- "any other JWT-federating system" escape hatch §27.3's own closing
-- paragraph describes ("a fourth target... is just a generic binding, not
-- a CP change").
CREATE TYPE cloud_identity_binding_kind AS ENUM ('aws', 'gcp', 'azure', 'generic');

-- scope_target_id is a single, polymorphic TEXT column, mirroring
-- provider_credentials.scope_target_id/sandbox_secrets.scope_target_id's
-- own exact convention:
--   - scope = 'environment': environments.id (migrations/
--     000021_environments.up.sql), stringified -- the IMMUTABLE id, not
--     any mutable name (environments has no name column at all, so there
--     is no ambiguity to resolve here -- confirmed against the real
--     schema, this Step's own gap-4 resolution).
--   - scope = 'global':      always NULL -- enforced by the CHECK
--     constraint below, identical to provider_credentials'/
--     sandbox_secrets' own scope=global convention.
--
-- audience is the caller-facing `aud` claim value a minted token carries
-- when this binding is the one that matched (§27.3: "aud = per-binding,
-- customer-set -- each cloud documents what it expects"). Free-form text,
-- not an enum -- every cloud's own expected value is a DIFFERENT literal
-- string (sts.amazonaws.com / a GCP workload-identity-provider resource
-- name / api://AzureADTokenExchange / whatever a generic consumer
-- expects), decided by the customer's own cloud-side config, not by this
-- schema.
--
-- params are IDENTIFIERS, never secrets (§27.3: "params are identifiers
-- not secrets... stored plaintext, readable, maintainer+ managed") -- AWS
-- role ARN; GCP workload-identity-provider resource name + optional
-- service-account email; Azure client id + tenant id; generic: the
-- env-var name to publish the token path under. Stored as plain JSONB,
-- NOT encrypted (unlike oidc_signing_keys.private_key_encrypted or any
-- provider_credentials/sandbox_secrets row) -- a role ARN or client id
-- grants nothing on its own without the customer's own cloud-side trust
-- policy naming Narvi's `sub`, so there is no secret-at-rest obligation
-- here the way there is for an actual credential value.
--
-- cloud_identity_bindings_no_azure_global CHECK: this Step's own gap-3
-- resolution, stated plainly and enforced structurally, not left
-- implicit. §27.3 is explicit that Azure's federated-credential matching
-- is EXACT-match-only on `sub`, and `sub` is always
-- narvi:environment:<environment_id> -- never anything global or
-- wildcard-shaped. A `kind='azure'` binding at `scope='global'` would
-- promise "this one role trusts every Environment", but Azure's own
-- trust config cannot actually express that in a single federated
-- credential -- the customer would need to hand-create one exact-match
-- federated credential PER Environment on the SAME app registration to
-- honor it, silently, with no error until the first Environment whose
-- federated credential was never added starts failing token exchange
-- cloud-side. Refusing the combination outright, at the schema layer (and
-- redundantly, at the Go validation layer -- internal/domain/
-- cloudidentity.ValidateBinding, defense in depth exactly like sandbox_
-- secrets_scope_target_id_shape's own CHECK/Go-validation pairing
-- elsewhere in this codebase) is the honest answer: aws/gcp/generic
-- bindings MAY be global-scoped (AWS role trust policies support
-- StringLike wildcard conditions on `sub`; GCP attribute-condition CEL
-- can pattern-match too -- both CAN honestly express "trust every
-- Environment" in ONE cloud-side policy), azure bindings may not.
CREATE TABLE cloud_identity_bindings (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope            cloud_identity_binding_scope NOT NULL,
    scope_target_id  TEXT,
    kind             cloud_identity_binding_kind NOT NULL,
    audience         TEXT NOT NULL,
    params           JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cloud_identity_bindings_scope_target_id_shape CHECK (
        (scope = 'global' AND scope_target_id IS NULL) OR
        (scope <> 'global' AND scope_target_id IS NOT NULL AND scope_target_id <> '')
    ),
    CONSTRAINT cloud_identity_bindings_audience_not_blank CHECK (audience <> ''),
    CONSTRAINT cloud_identity_bindings_no_azure_global CHECK (
        NOT (kind = 'azure' AND scope = 'global')
    )
);

-- At most one binding per (scope, scope_target_id, kind) -- §27.3's own
-- "at most one per (scope target, kind) in v1". Same pair-of-partial-
-- unique-indexes idiom provider_credentials/sandbox_secrets already
-- establish (NULLs-are-distinct means a plain UNIQUE constraint would
-- never collide across two global-scoped rows), reused verbatim per this
-- Step's own brief.
CREATE UNIQUE INDEX cloud_identity_bindings_scoped_uniq
    ON cloud_identity_bindings (scope, scope_target_id, kind)
    WHERE scope_target_id IS NOT NULL;
CREATE UNIQUE INDEX cloud_identity_bindings_global_uniq
    ON cloud_identity_bindings (kind)
    WHERE scope_target_id IS NULL;

-- Lookup index for the minting endpoint's own resolution query: every
-- candidate binding (global, plus this session's own environment) whose
-- audience matches the requested one -- mirrors provider_credentials_
-- scope_target_id_idx/sandbox_secrets_scope_target_id_idx's own identical
-- reasoning and shape.
CREATE INDEX cloud_identity_bindings_scope_target_id_idx
    ON cloud_identity_bindings (scope_target_id)
    WHERE scope_target_id IS NOT NULL;
