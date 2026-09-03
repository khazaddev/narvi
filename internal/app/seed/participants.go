package seed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/auditlog"
	"github.com/narvidev/narvi/internal/domain/seedmanifest"
)

// participantKey renders p as a safe, human-readable Item.Key -- GitHub
// id + email, never anything secret.
func participantKey(p seedmanifest.Participant) string {
	return fmt.Sprintf("github:%d (%s)", p.GitHubID, p.Email)
}

// resolveInitialRole decides admin-vs-member for a BRAND NEW participant,
// exactly mirroring internal/adapters/inbound/auth's own
// createUserAndIdentity: a case-insensitive match of email against
// initialAdminEmails, nothing else. This is the ONLY place in this
// package a role is ever decided -- see seedmanifest's own doc comment
// for why the manifest itself carries no role field at all.
func resolveInitialRole(email string, initialAdminEmails []string) sqlcgen.UserRole {
	for _, adminEmail := range initialAdminEmails {
		if strings.EqualFold(adminEmail, email) {
			return sqlcgen.UserRoleAdmin
		}
	}
	return sqlcgen.UserRoleMember
}

// seedParticipant maps ONE participant to a user, create-only (see doc.go
// for the full "why create-once" reasoning). Looks up the GitHub identity
// FIRST (never the email, never a login) -- an existing identity means
// this participant is already represented, and this function returns
// immediately, having touched nothing: not the user's role, not their
// display name, not any other column. This is what makes "never silently
// escalate an existing user" true regardless of what dryRun is or what
// the manifest says.
func seedParticipant(ctx context.Context, deps Deps, p seedmanifest.Participant, dryRun bool) Item {
	key := participantKey(p)
	externalID := strconv.FormatInt(p.GitHubID, 10)

	existing, err := deps.Identities.GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderGithub, externalID)
	if err == nil {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeSkipped,
			Detail: fmt.Sprintf("github id already linked to user %s; role untouched", existing.UserID.String())}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "look up existing identity: " + err.Error()}
	}

	role := resolveInitialRole(p.Email, deps.InitialAdminEmails)

	if dryRun {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeWouldCreate, Detail: "role=" + string(role)}
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "begin tx: " + err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	createdUser, err := deps.Users.WithTx(tx).Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: p.Email,
		DisplayName:  p.DisplayName,
		Role:         role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Item{Kind: "participant", Key: key, Outcome: OutcomeError,
				Detail: "email already belongs to a different user; link this GitHub identity via the Members API instead of re-running seed"}
		}
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "create user: " + err.Error()}
	}

	// linked_via="admin" is a deliberate reuse of
	// internal/adapters/inbound/auth's own identical choice for a
	// first-time identity creation with no real linking algorithm behind
	// it (see that package's own createUserAndIdentity doc comment) --
	// not this package inventing a new meaning for the enum value.
	if _, err := deps.Identities.WithTx(tx).Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:        createdUser.ID,
		Provider:      sqlcgen.IdentityProviderGithub,
		ExternalID:    externalID,
		Email:         &p.Email,
		EmailVerified: false, // seeded, never verified by a real OAuth round trip
		LinkedVia:     sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		if isUniqueViolation(err) {
			return Item{Kind: "participant", Key: key, Outcome: OutcomeError,
				Detail: "github id was linked concurrently (likely a real sign-in racing this run); safe to re-run seed, it will now skip this entry"}
		}
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "create identity: " + err.Error()}
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), systemActor(), "seed.participant_created", "user", createdUser.ID.String(), map[string]any{
		"role":      string(role),
		"github_id": p.GitHubID,
	}); err != nil {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "record audit log: " + err.Error()}
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{Kind: "participant", Key: key, Outcome: OutcomeError, Detail: "commit tx: " + err.Error()}
	}

	return Item{Kind: "participant", Key: key, Outcome: OutcomeCreated, Detail: "role=" + string(role)}
}
