-- cluster_bindings: the target Kubernetes cluster for an Environment
-- (Step 73b, "cloud identity: sandbox-side consumption + kubeconfig
-- injection", §27.4). "The target cluster" is selected the way §14
-- already models deployment targets: per-Environment. §27.4's own shape,
-- verbatim: "cluster_bindings(environment_id UNIQUE, name, server_url,
-- ca_bundle, auth_kind ENUM('cloud','oidc','static'), params JSONB), one
-- cluster per Environment in v1 (the bullet's own singular)."
--
-- Unlike cloud_identity_bindings (migrations/
-- 000093_cloud_identity_bindings.up.sql), this table has NO scope column
-- at all -- v1 is environment-scoped only, no global fallback (§27.4
-- names no global rung, and a cluster is a genuinely per-deployment-target
-- concept, unlike a cloud role grant that can sensibly apply to "every
-- Environment"). environment_id is UNIQUE (not merely indexed) -- "at
-- most one cluster per Environment", enforced structurally, matching
-- opencode_configs' own "at most one row per scope target" partial-unique
-- idiom, simplified further since there is exactly one scope here.
--
-- cluster_binding_auth_kind: the 3 auth rungs §27.4 names, in PREFERENCE
-- order (cloud > oidc > static) -- this column just records which rung a
-- binding declares; sandbox-agent (cmd/sandbox-agent/kubeconfig.go)
-- renders the kubeconfig differently per rung, it never itself picks a
-- rung a customer didn't ask for.
CREATE TYPE cluster_binding_auth_kind AS ENUM ('cloud', 'oidc', 'static');

-- name is the cluster's own name, as far as sandbox-agent's rendered
-- kubeconfig is concerned: BOTH a human-readable label AND (for
-- auth_kind='cloud') the literal cluster-name argument the cloud's own
-- exec-credential plugin needs (`aws eks get-token --cluster-name
-- <name>`; GKE/AKS's own plugins take their cluster identity from the
-- kubeconfig's server/exec args, not a second name field) -- one column,
-- not two, since every real caller needs the SAME string either way.
--
-- server_url/ca_bundle are the kubeconfig `cluster` stanza's own two
-- fields (API server endpoint, PEM-encoded CA certificate) -- NULLABLE,
-- because auth_kind='static' (see below) supplies an ALREADY-COMPLETE
-- kubeconfig document (its own server_url/ca_bundle already inside it)
-- via a referenced sandbox_secrets row, making this table's own copies
-- redundant for that one rung; auth_kind IN ('cloud','oidc') both render
-- a `cluster` stanza FROM these two columns and therefore require them
-- (cluster_bindings_server_ca_required_for_cloud_oidc CHECK, below).
--
-- params carries auth_kind-specific IDENTIFIERS, never secrets -- the
-- SAME "identifiers not secrets, plaintext, readable, maintainer+
-- managed" posture cloud_identity_bindings.params already establishes
-- (that migration's own top comment), extended here per rung:
--   - auth_kind='cloud':  {"cloud": "aws"|"gcp"|"azure"[, "region": "..."
--     (AWS only, optional -- `aws eks get-token`'s own optional
--     --region flag)]} -- which of the three exec-credential plugins
--     (§27.4: "aws eks get-token / gke-gcloud-auth-plugin / kubelogin")
--     the rendered kubeconfig's own exec stanza invokes. The plugin
--     itself then rides §27.3's already-established env vars
--     (AWS_WEB_IDENTITY_TOKEN_FILE+AWS_ROLE_ARN / GOOGLE_APPLICATION_
--     CREDENTIALS / AZURE_FEDERATED_TOKEN_FILE+ids) -- Narvi implements
--     no per-cloud STS exchange code here either, exactly like §27.3.
--   - auth_kind='oidc':   {"clientId": "<the audience/client id
--     kube-apiserver's own --oidc-client-id trusts>"} -- the exec
--     plugin is sandbox-agent's OWN `kube-credential` subcommand
--     (cmd/sandbox-agent/main.go's runKubeCredentialHelper), which mints
--     a fresh §27.3 token with `aud` = this clientId on every invocation
--     and prints it as a standard ExecCredential JSON document.
--   - auth_kind='static': {"secretName": "<the sandbox_secrets NAME
--     (Step 72, migrations/000090_sandbox_secrets.up.sql) whose VALUE is
--     the complete, already-server_url/ca_bundle-bearing kubeconfig file
--     content>"} -- §27.4, verbatim: "an uploaded kubeconfig for
--     clusters with no OIDC path at all, stored as a §27.1 secret (the
--     value is the file content; delivered and written to disk by
--     sandbox-agent, never env-var-expanded)." sandbox-agent looks
--     secretName up in the SAME resolved sandbox_secrets map it already
--     fetches at boot (cmd/sandbox-agent/sandboxsecrets.go) -- no second
--     delivery round trip for this rung.
CREATE TABLE cluster_bindings (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id TEXT NOT NULL,
    name           TEXT NOT NULL,
    server_url     TEXT,
    ca_bundle      TEXT,
    auth_kind      cluster_binding_auth_kind NOT NULL,
    params         JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cluster_bindings_environment_id_not_blank CHECK (environment_id <> ''),
    CONSTRAINT cluster_bindings_name_not_blank CHECK (name <> ''),
    -- auth_kind='static' supplies its own complete kubeconfig document
    -- (see params' own doc comment above) -- server_url/ca_bundle are
    -- ONLY required for the two rungs that actually render a `cluster`
    -- stanza from them.
    CONSTRAINT cluster_bindings_server_ca_required_for_cloud_oidc CHECK (
        auth_kind = 'static' OR (server_url IS NOT NULL AND server_url <> '' AND ca_bundle IS NOT NULL AND ca_bundle <> '')
    )
);

-- "one cluster per Environment in v1" (§27.4, verbatim) -- enforced
-- structurally, not by convention: a second PUT for the same
-- environment_id is an UPSERT (see queries/clusterbindings.sql), never a
-- second row.
CREATE UNIQUE INDEX cluster_bindings_environment_id_uniq ON cluster_bindings (environment_id);
