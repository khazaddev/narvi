-- automations: triggers & extras (Step 52, §8.4) -- ALTERs the deliberately
-- minimal automations table Step 51 left behind (migrations/
-- 000051_automations.up.sql's own doc comment: "Step 52 ... owns trigger
-- conditions (GitHub/Linear/webhook/cron), sandboxSettings, and
-- per-automation secrets, and will ALTER this table to add them"). Three
-- independent groups of columns, each documented on its own:
--
-- 1) trigger_type/trigger_config/webhook_token_hash/last_cron_fired_at --
--    WHAT causes an invocation to be created for this automation (this
--    Step's own condition builder). trigger_type is a closed enum; every
--    type-specific detail (a cron schedule, a GitHub event/action/label
--    filter, a Linear event/action/team filter) lives in trigger_config
--    JSONB, never a type-specific column -- mirrors this codebase's own
--    established "JSONB for array/variant-shaped config, validated in Go
--    before it is ever persisted" precedent (repos, targets above).
--    'manual' is the default for every automation created before this
--    migration (backfilled) and remains a legitimate value going forward:
--    an automation with no automatic trigger, fired only via a direct
--    CreateInvocation call (this package's own integration tests today; a
--    future explicit "Run now" UI action later) -- internal/app/automation's
--    own fan-out/reconcile/sweep pumps have never gated on trigger_type at
--    all (invocationenqueue.go's CreateInvocation is, and remains, the one
--    low-level "an invocation now exists" primitive every trigger path
--    funnels through, never bypassed).
--
--    webhook_token_hash is this automation's own inbound-webhook bearer
--    credential, hashed at rest via platform.HashToken -- the SAME
--    SHA-256, unsalted, hex-encoded convention ws_tokens.token_hash already
--    established (internal/platform/tokenhash.go's own doc comment: "a
--    high-entropy, server-generated secret, not a low-entropy human
--    password"). NULL for every trigger_type other than 'webhook'. This is
--    NOT a "per-automation secret" in Step 53's sense (that Step's own
--    scope is injecting a THIRD PARTY's credential, e.g. a provider API
--    key, INTO the sandbox/agent process) -- it authenticates an INBOUND
--    caller reaching Narvi's own control plane, exactly like ws_tokens/
--    sandboxes.token_hash already do, so it is minted/verified with the
--    same existing mechanism rather than waiting on Step 53's own
--    secret-storage design.
--
--    last_cron_fired_at is the CAS guard backing the cron trigger pump
--    (internal/app/automation's own new runTriggerPump): "has this
--    automation already fired for the current UTC minute bucket" --
--    compared and set against a minute-truncated instant, so a tick that
--    lands more than once inside the same matching minute (clock jitter, a
--    slow previous tick, a second pod's own concurrent pump) never
--    double-fires the same scheduled minute, mirroring fanned_out_at's own
--    "UPDATE ... WHERE <guard column> IS NULL/stale" CAS idiom, just keyed
--    on a recurring minute bucket instead of a one-way NULL flip (a cron
--    trigger, unlike a fan-out claim, is expected to fire again and again
--    forever).
--
-- 2) sandbox_path_scope/sandbox_mock_configured/sandbox_contracts_path --
--    §8.4's own "sandboxSettings honored on automation sessions": the
--    EXACT SAME three attributes environments already carries (migrations/
--    000021_environments.up.sql, 000025_mock_config_contract_drift.up.sql),
--    just namespaced onto automations directly rather than requiring a
--    separately-managed Environment row to reference by id (no such
--    standalone Environment CRUD/reuse-by-id surface exists anywhere in
--    this codebase yet -- see 000021's own scope note, still true here).
--    internal/app/automation's own fanout.go now threads these through to
--    httpapi.CreateSessionOnTx's existing pathScope/mockConfig request
--    fields for every run it creates, the SAME code path an ordinary
--    scoped web session already goes through -- automation-created
--    sessions were creating a bare, unscoped restdtos.CreateSessionRequest
--    before this Step (fanout.go's own createRunAndSession only ever set
--    SpawnSource/Repos/Prompt/Title), silently ignoring any sandbox
--    scoping a maintainer might want an automation's own sessions to
--    respect.
--
-- 3) env_vars -- §8.4's own "per-automation env vars" (explicitly NOT
--    secrets -- see this migration's own trailing comment below, and
--    internal/domain/automation/doc.go's own deferral writeup). A JSONB
--    array of {name, value} objects, the SAME array-of-object JSONB shape
--    `repos`/`targets` already establish -- plain, non-sensitive
--    configuration (e.g. a feature-flag name, a target environment label)
--    threaded into each run's dispatched turn, never treated as
--    confidential.
--
-- 4) last_run_at/last_run_status/artifact_summary -- §8.4's own "last_run +
--    artifact_summary populated": internal/app/automation's own closeout.go
--    now writes these three columns onto the parent automations row the
--    moment it closes that automation's own most recent invocation (the
--    SAME closeInvocation call site that already resets/increments
--    consecutive_failures) -- last_run_status reuses
--    automation_invocation_status verbatim (never a new, parallel
--    taxonomy), and artifact_summary is a short, MECHANICALLY generated
--    one-line sentence from that invocation's own already-persisted run
--    outcomes (target names + succeeded/failed counts) -- see internal/
--    domain/automation's own summary.go for why this is a deterministic
--    template over existing typed data, not a model-authored narrative via
--    a new agent-facing verdict-posting tool (that would be a materially
--    larger, separate feature, out of this Step's own scope).
CREATE TYPE automation_trigger_type AS ENUM ('manual', 'cron', 'github', 'linear', 'webhook');

ALTER TABLE automations ADD COLUMN trigger_type automation_trigger_type NOT NULL DEFAULT 'manual';
ALTER TABLE automations ADD COLUMN trigger_config JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE automations ADD COLUMN webhook_token_hash TEXT;
ALTER TABLE automations ADD COLUMN last_cron_fired_at TIMESTAMPTZ;

ALTER TABLE automations ADD COLUMN sandbox_path_scope JSONB;
ALTER TABLE automations ADD COLUMN sandbox_mock_configured BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE automations ADD COLUMN sandbox_contracts_path TEXT;

-- Plain config, never a secret -- Step 53 ("provider credential
-- injection", §25.1/§25.3) is explicitly documented (docs/
-- IMPLEMENTATION_PLAN.md row 53) as "the first Step that actually builds"
-- generic secret storage anywhere in this codebase; per-automation
-- SECRETS remain deliberately UNIMPLEMENTED here -- no column, no table --
-- until that lands, at which point a small, focused follow-up extends
-- secret scoping to automations alongside repo/environment/global (see
-- internal/domain/automation/doc.go's own deferral writeup for the full
-- reasoning). env_vars below is plain, non-confidential key/value config
-- only.
ALTER TABLE automations ADD COLUMN env_vars JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE automations ADD COLUMN last_run_at TIMESTAMPTZ;
ALTER TABLE automations ADD COLUMN last_run_status automation_invocation_status;
ALTER TABLE automations ADD COLUMN artifact_summary TEXT;

-- Backs the webhook trigger's own inbound-auth lookup (GetAutomationByWebhookTokenHash,
-- queries/automations.sql) -- partial (WHERE webhook_token_hash IS NOT
-- NULL) so the overwhelming majority of rows (every non-webhook-triggered
-- automation) never occupy space in it, and so this constraint never
-- fires against the many legitimately-NULL rows a plain UNIQUE column
-- constraint would otherwise compare against each other.
CREATE UNIQUE INDEX automations_webhook_token_hash_uniq ON automations (webhook_token_hash) WHERE webhook_token_hash IS NOT NULL;

-- Backs the cron trigger pump's own per-tick scan (ListActiveCronAutomations,
-- queries/automations.sql): every active, cron-triggered automation --
-- expected to stay a small table, but indexed anyway since this query now
-- runs every AutomationEnginePumpInterval tick (60s), forever.
CREATE INDEX automations_cron_trigger_idx ON automations (id) WHERE trigger_type = 'cron' AND status = 'active';
