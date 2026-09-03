package seed

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
)

// Deps bundles every store/config value Run needs. Built once by
// cmd/control-plane's own seed subcommand, from the SAME platform.Load()
// config and *pgxpool.Pool "control-plane serve" itself uses -- this
// tool is not a lower-privilege actor than the control plane; it needs
// (and is trusted with) the same DB credentials and the same
// TokenEncryptionKey, mirroring a migration or any other operator-run,
// direct-DB-access tool's own trust tier. There is deliberately no
// domain/authz.Authorize call anywhere in this package: that check
// exists to gate an authenticated HTTP actor against another actor's own
// narrower role, and this tool has no HTTP request/authenticated actor
// to check in the first place -- see doc.go's own "audit log" section
// for how a system-driven write is still attributed.
type Deps struct {
	Pool *pgxpool.Pool

	Users        *postgres.UserStore
	Identities   *postgres.IdentityStore
	Secrets      *postgres.SandboxSecretStore
	Automations  *postgres.AutomationStore
	RepoSettings *postgres.RepoSettingsStore
	AuditLog     *postgres.AuditLogStore

	// Sandboxes (§30.4) is seedRepoSetting's own input to
	// internal/app/repodemotion.Sweep -- the ONLY use this package makes
	// of it -- called once a real repo_settings.live_egress_enabled
	// true->false transition commits, so every currently-live sandbox of
	// the just-demoted repo is flagged for real termination and has any
	// in-flight push signal cancelled. See reposettings.go's own doc
	// comment.
	Sandboxes *postgres.SandboxStore

	// TokenEncryptionKey is platform.Config.TokenEncryptionKey, used to
	// seal every Secret's plaintext value via platform.EncryptToken
	// before it is ever written to sandbox_secrets.value_encrypted --
	// see secrets.go.
	TokenEncryptionKey []byte

	// InitialAdminEmails is platform.Config.InitialAdminEmails -- the
	// ONLY source this package consults to decide a newly-created
	// participant's role. See seedmanifest's own doc comment and
	// participants.go.
	InitialAdminEmails []string
}

// NewDeps builds a Deps from an already-open pool and the config values
// Run needs -- a thin constructor kept separate from Deps' own struct
// literal purely so cmd/control-plane/seed.go reads as one call, mirroring
// this codebase's own NewXStore constructor convention throughout
// internal/adapters/outbound/postgres.
func NewDeps(pool *pgxpool.Pool, tokenEncryptionKey []byte, initialAdminEmails []string) Deps {
	return Deps{
		Pool:               pool,
		Users:              postgres.NewUserStore(pool),
		Identities:         postgres.NewIdentityStore(pool),
		Secrets:            postgres.NewSandboxSecretStore(pool),
		Automations:        postgres.NewAutomationStore(pool),
		RepoSettings:       postgres.NewRepoSettingsStore(pool),
		AuditLog:           postgres.NewAuditLogStore(pool),
		Sandboxes:          postgres.NewSandboxStore(pool),
		TokenEncryptionKey: tokenEncryptionKey,
		InitialAdminEmails: initialAdminEmails,
	}
}

// uniqueViolationCode is Postgres' own SQLSTATE for a unique-constraint
// violation -- mirrors internal/adapters/inbound/httpapi's own identical
// constant (members.go) and internal/app/identitylink's own
// isUniqueViolation (service.go); each package keeps its own copy rather
// than sharing one across an inbound-adapter/app-layer boundary, exactly
// like those two already do.
const uniqueViolationCode = "23505"

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) -- used by this package's own create-if-
// absent paths (participants.go, secrets.go, automations.go) to turn a
// lost create-vs-create race into a clean "already exists" outcome
// instead of a raw error, mirroring httpapi.LinkMemberIdentity's own
// "lost the race, resolve the winner" precedent.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// systemActor is the actorUserID every audit_log row this package writes
// carries: an explicit invalid pgtype.UUID{}, NULL at the database layer
// -- see doc.go's own "audit log" section for why (a batch import has no
// single human actor, unlike an OAuth sign-in's own self-attributed
// "user.created" row).
func systemActor() pgtype.UUID {
	return pgtype.UUID{}
}
