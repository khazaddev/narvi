package seed

import (
	"context"
	"fmt"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/domain/sandboxsecret"
	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
	"github.com/khazaddev/narvi/internal/platform"
)

// secretKey renders s as a safe, human-readable Item.Key -- scope, repo
// (if any), and NAME only. Never the value: this function's own return
// is placed directly into a Report the caller may print to a terminal,
// log aggregator, or CI output -- see report.go's own doc comment.
func secretKey(s seedmanifest.Secret) string {
	if s.Scope == seedmanifest.SecretScopeRepo {
		return fmt.Sprintf("repo:%s:%s", s.RepoFullName, s.Name)
	}
	return fmt.Sprintf("global:%s", s.Name)
}

// secretScopeTarget converts a manifest Secret's scope into the
// (sqlcgen.SandboxSecretScope, *string) pair SandboxSecretStore's own
// methods take -- nil scopeTargetID for global, matching
// SandboxSecretStore.Create's own "nil is global" convention exactly.
func secretScopeTarget(s seedmanifest.Secret) (sqlcgen.SandboxSecretScope, *string) {
	if s.Scope == seedmanifest.SecretScopeRepo {
		repo := s.RepoFullName
		return sqlcgen.SandboxSecretScopeRepo, &repo
	}
	return sqlcgen.SandboxSecretScopeGlobal, nil
}

// secretAlreadyExists reports whether a sandbox_secrets row named s.Name
// already exists at s's own (scope, scopeTargetID) -- a plain read, used
// by both the dry-run path and the real-run path below, so both compute
// the exact same "would this create or skip" decision.
func secretAlreadyExists(ctx context.Context, deps Deps, s seedmanifest.Secret) (bool, error) {
	scope, scopeTargetID := secretScopeTarget(s)
	rows, err := deps.Secrets.ListByScope(ctx, scope, scopeTargetID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Name == s.Name {
			return true, nil
		}
	}
	return false, nil
}

// seedSecret creates one sandbox_secrets row, create-if-absent (see
// doc.go for why secrets are never reconciled/overwritten by this tool).
// Every write goes through the SAME two mechanisms this codebase already
// uses for every other sandbox secret: internal/domain/sandboxsecret.
// ValidateName (fail-closed name validation -- NARVI_*/OPENCODE_*/
// provider-credential/cloud-identity/cluster-binding names all rejected)
// and platform.EncryptToken (AES-256-GCM at rest) -- there is no code
// path here that writes a row without going through both, matching
// internal/adapters/inbound/httpapi/sandboxsecrets.go's own
// createSandboxSecret exactly. s.Value is read exactly once (to encrypt
// it) and never appears in the returned Item, a log line, or an error
// message this function produces.
func seedSecret(ctx context.Context, deps Deps, s seedmanifest.Secret, dryRun bool) Item {
	key := secretKey(s)

	exists, err := secretAlreadyExists(ctx, deps, s)
	if err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "check existing: " + err.Error()}
	}
	if exists {
		outcome := OutcomeSkipped
		if dryRun {
			outcome = OutcomeWouldSkip
		}
		return Item{Kind: "secret", Key: key, Outcome: outcome, Detail: "a secret with this name already exists at this scope"}
	}

	if dryRun {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeWouldCreate}
	}

	// Re-validated here, immediately before write -- defense in depth,
	// the SAME function seedmanifest.Validate already called at load
	// time, never a second/independent check (see sandboxsecret's own
	// "one owning mechanism per env-var name" rule, referenced from this
	// codebase's Step 72/73b work).
	if err := sandboxsecret.ValidateName(s.Name); err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "invalid secret name: " + err.Error()}
	}

	encrypted, err := platform.EncryptToken(deps.TokenEncryptionKey, []byte(s.Value))
	if err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "encrypt value failed"}
	}

	scope, scopeTargetID := secretScopeTarget(s)

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "begin tx: " + err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := deps.Secrets.WithTx(tx).Create(ctx, scope, scopeTargetID, s.Name, encrypted)
	if err != nil {
		if isUniqueViolation(err) {
			return Item{Kind: "secret", Key: key, Outcome: OutcomeSkipped, Detail: "created concurrently by another writer"}
		}
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "create failed"}
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), systemActor(), "seed.secret_created", "sandbox_secret", created.ID.String(), map[string]any{
		"scope": string(scope),
		"name":  s.Name,
	}); err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "record audit log: " + err.Error()}
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{Kind: "secret", Key: key, Outcome: OutcomeError, Detail: "commit tx: " + err.Error()}
	}

	return Item{Kind: "secret", Key: key, Outcome: OutcomeCreated}
}
