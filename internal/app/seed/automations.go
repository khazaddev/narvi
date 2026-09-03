package seed

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/auditlog"
	"github.com/narvidev/narvi/internal/domain/seedmanifest"
	"github.com/narvidev/narvi/internal/platform"
)

// wireRepoTarget/wireEnvVar are this package's OWN private JSON shapes
// for the automations.repos/env_vars JSONB columns -- deliberately NOT
// contracts/gen/go/restdtos types: internal/app/seed sits at the same
// layer as internal/app/automation, which keeps itself contracts-
// independent on purpose (internal/domain/automation/target.go's own doc
// comment: "so this package stays adapter/contracts-independent, §11").
// The json tags below match restdtos.AutomationReposElem/
// AutomationEnvVarElem's own tags byte-for-byte, so a row this package
// creates reads back through internal/adapters/inbound/httpapi's own
// automationToDTO identically to one created through the REST API.
type wireRepoTarget struct {
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Branch *string `json:"branch"`
}

type wireEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type wireCronTriggerConfig struct {
	Schedule string `json:"schedule"`
}

// automationKey renders a's Name as the Item.Key -- Name is this
// resource's own idempotency key (see doc.go: automations have no other
// natural key, and no Update method exists to reconcile by).
func automationKey(a seedmanifest.Automation) string { return a.Name }

// seedAutomation creates one automations row, create-if-absent, matched
// by Name against existingByName (built ONCE per Run call by run.go --
// see that file's own doc comment on why one List call, not one per
// item). See doc.go for why this is the only sound idempotency choice
// for this table.
func seedAutomation(ctx context.Context, deps Deps, a seedmanifest.Automation, existingByName map[string]sqlcgen.Automation, dryRun bool) Item {
	key := automationKey(a)

	if existing, ok := existingByName[a.Name]; ok {
		outcome := OutcomeSkipped
		if dryRun {
			outcome = OutcomeWouldSkip
		}
		return Item{Kind: "automation", Key: key, Outcome: outcome,
			Detail: fmt.Sprintf("already exists as automation id=%s; this tool never updates an existing automation", existing.ID.String())}
	}

	if dryRun {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeWouldCreate, Detail: "trigger=" + string(a.TriggerType)}
	}

	reposWire := make([]wireRepoTarget, len(a.Repos))
	for i, r := range a.Repos {
		wr := wireRepoTarget{Name: r.Name, URL: r.URL}
		if r.Branch != "" {
			b := r.Branch
			wr.Branch = &b
		}
		reposWire[i] = wr
	}
	reposJSON, err := json.Marshal(reposWire)
	if err != nil {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "marshal repos: " + err.Error()}
	}

	var triggerConfigJSON []byte
	switch a.TriggerType {
	case seedmanifest.AutomationTriggerCron:
		triggerConfigJSON, err = json.Marshal(wireCronTriggerConfig{Schedule: a.CronSchedule})
		if err != nil {
			return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "marshal trigger config: " + err.Error()}
		}
	default: // manual, webhook
		triggerConfigJSON = []byte("{}")
	}

	var sandboxPathScopeJSON []byte
	if len(a.PathScope) > 0 {
		sandboxPathScopeJSON, err = json.Marshal(a.PathScope)
		if err != nil {
			return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "marshal path scope: " + err.Error()}
		}
	}
	var sandboxContractsPath *string
	if a.MockConfigured {
		cp := a.ContractsPath
		sandboxContractsPath = &cp
	}

	envVarsWire := make([]wireEnvVar, len(a.EnvVars))
	for i, v := range a.EnvVars {
		envVarsWire[i] = wireEnvVar{Name: v.Name, Value: v.Value}
	}
	envVarsJSON, err := json.Marshal(envVarsWire)
	if err != nil {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "marshal env vars: " + err.Error()}
	}

	// Webhook token: minted the SAME way httpapi.CreateAutomation does
	// (platform.GenerateToken + platform.HashToken), returned to the
	// operator via Item.Detail EXACTLY ONCE -- the one sanctioned
	// exception to "an Item never carries a secret" (report.go's own doc
	// comment): mirrors MintWSToken/CreateAutomation's own identical
	// "hashed at rest, plaintext surfaced exactly once at creation"
	// convention. Never logged anywhere else, and dry-run never reaches
	// this branch (nothing is minted for a run that writes nothing).
	var webhookTokenHash *string
	var webhookDetail string
	if a.TriggerType == seedmanifest.AutomationTriggerWebhook {
		token, terr := platform.GenerateToken()
		if terr != nil {
			return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "generate webhook token failed"}
		}
		hash := platform.HashToken(token)
		webhookTokenHash = &hash
		webhookDetail = "webhook token (shown once): " + token
	}

	var promptCol *string
	if a.Prompt != "" {
		p := a.Prompt
		promptCol = &p
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "begin tx: " + err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := deps.Automations.WithTx(tx).Create(ctx, sqlcgen.CreateAutomationParams{
		Name:                  a.Name,
		Prompt:                promptCol,
		Repos:                 reposJSON,
		CreatedBy:             systemActor(),
		TriggerType:           sqlcgen.AutomationTriggerType(a.TriggerType),
		TriggerConfig:         triggerConfigJSON,
		WebhookTokenHash:      webhookTokenHash,
		SandboxPathScope:      sandboxPathScopeJSON,
		SandboxMockConfigured: a.MockConfigured,
		SandboxContractsPath:  sandboxContractsPath,
		EnvVars:               envVarsJSON,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Item{Kind: "automation", Key: key, Outcome: OutcomeSkipped, Detail: "created concurrently by another writer"}
		}
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "create failed"}
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), systemActor(), "seed.automation_created", "automation", created.ID.String(), map[string]any{
		"name":         a.Name,
		"trigger_type": string(a.TriggerType),
	}); err != nil {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "record audit log: " + err.Error()}
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{Kind: "automation", Key: key, Outcome: OutcomeError, Detail: "commit tx: " + err.Error()}
	}

	return Item{Kind: "automation", Key: key, Outcome: OutcomeCreated, Detail: webhookDetail}
}
