package identitylink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	domainidentitylink "github.com/khazaddev/narvi/internal/domain/identitylink"
	"github.com/khazaddev/narvi/internal/platform"
)

// uniqueViolationCode is Postgres' own SQLSTATE for a unique-constraint
// violation -- used below to recognize the one race this package's own
// two INSERTs can lose to a concurrent winner (identities.
// identities_provider_external_id_key, migrations/000003_identities.up.sql),
// without needing a second, separate SELECT-then-check round trip on
// every call's own happy path.
const uniqueViolationCode = "23505"

// MagicLinkPath is the route internal/adapters/inbound/identitylink
// mounts its magic-link consume handler at (cmd/control-plane/main.go:
// router.Get(identitylink.MagicLinkPath+"{nonce}", ...)) -- defined HERE,
// in the app-layer package, rather than in that inbound package itself,
// so BuildMagicLinkURL below and the inbound package's own route
// registration can never drift apart (the inbound package imports this
// one, never the reverse -- see this package's own doc.go).
const MagicLinkPath = "/auth/identity-link/"

// BuildMagicLinkURL renders the full, clickable magic-link URL for nonce
// -- publicBaseURL is platform.Config.PublicBaseURL, the SAME base every
// other externally-reachable URL this codebase constructs uses (e.g.
// auth.NewGitHubOAuthConfig's own RedirectURL).
func BuildMagicLinkURL(publicBaseURL, nonce string) string {
	return publicBaseURL + MagicLinkPath + nonce
}

// Deps bundles every store/config Resolve/Consume need -- mirrors this
// codebase's own established "Deps struct, not a long positional
// parameter list" precedent (internal/adapters/inbound/slack.Deps et al).
type Deps struct {
	Pool        *pgxpool.Pool
	Users       *postgres.UserStore
	Identities  *postgres.IdentityStore
	LinkPrompts *postgres.IdentityLinkPromptStore
	AuditLog    *postgres.AuditLogStore

	PublicBaseURL string
	PromptTTL     time.Duration
}

// Resolution is Resolve's own verdict for one inbound event from
// (provider, externalID) -- see this package's own doc.go for the full
// algorithm each field corresponds to.
type Resolution struct {
	// UserID is Valid iff this (provider, externalID) identity is now
	// known to belong to a real user -- either it already did (the fast
	// path, no provider API call at all) or Resolve just auto-linked it
	// THIS call. Invalid means: keep bot attribution, exactly as before.
	UserID pgtype.UUID

	// AutoLinked is true iff THIS call performed a BRAND NEW auto-link
	// (as opposed to UserID being Valid because the identity was already
	// linked coming in) -- the caller's own signal to post the
	// in-channel "connected your account" notice (§13.2 step 3).
	AutoLinked bool

	// MagicLinkURL is non-empty iff a NEW link prompt was just minted
	// this call -- the caller's own signal to post it in-channel (§13.2
	// step 4). Empty on every other outcome, INCLUDING when a still-live
	// prompt from an earlier call already exists (see service.go's own
	// "never re-mint/re-send on every message" policy) -- the caller
	// must not re-post a magic link it has already posted before.
	MagicLinkURL string
}

// NotificationText renders the §13.2 step-3/4 "notify the user in-channel"
// text for r, or "" when there's nothing to say (the identity was already
// linked coming in, the fetch failed/found no email, or a still-live
// prompt from an earlier call was silently reused). Centralized here, not
// duplicated per-provider, so the wording an ingress caller posts into
// Slack/Linear never drifts between the two -- each caller decides WHERE
// to put this text (appended to an existing outbound message, a fresh
// one, or skipped entirely when no natural channel/thread context exists
// for it), never what it says.
func (r Resolution) NotificationText() string {
	switch {
	case r.AutoLinked:
		return "I've connected this to your Narvi account."
	case r.MagicLinkURL != "":
		return "I couldn't automatically match this to a Narvi account. Connect it here: " + r.MagicLinkURL
	default:
		return ""
	}
}

// Resolve implements §13.2's auto-linking algorithm for one inbound event
// from (provider, externalID) -- see this package's own doc.go for the
// complete step-by-step design. email/emailOK is the CALLER's own
// already-fetched-and-retried provider profile email (see that doc
// comment for why fetching itself is not this function's job).
func Resolve(ctx context.Context, deps Deps, provider sqlcgen.IdentityProvider, externalID, email string, emailOK bool) (Resolution, error) {
	existing, err := deps.Identities.GetByProviderAndExternalID(ctx, provider, externalID)
	if err == nil {
		return Resolution{UserID: existing.UserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Resolution{}, fmt.Errorf("identitylink: look up existing identity: %w", err)
	}

	if !emailOK {
		// The caller's own fetch (already retried) never succeeded --
		// §13.2's own rule is that this is a retryable failure, not an
		// empty identity: never guess, never null anything out, just
		// leave this attempt for a LATER event from the same identity to
		// try again. Bot attribution, nothing else to do.
		return Resolution{}, nil
	}

	matchedUserIDs, err := matchUserIDs(ctx, deps, email)
	if err != nil {
		return Resolution{}, fmt.Errorf("identitylink: match user ids: %w", err)
	}

	if userIDStr, ok := domainidentitylink.Decide(matchedUserIDs); ok {
		return autoLink(ctx, deps, provider, externalID, email, userIDStr)
	}

	return createOrReuseLinkPrompt(ctx, deps, provider, externalID)
}

// matchUserIDs runs §13.2 step 2's own two lookups (users.primary_email,
// verified identities.email) and returns the DEDUPLICATED union of user
// ids either matched -- the same user matching both ways at once (e.g.
// their GitHub-derived primary_email happens to equal their own Slack
// profile email, which is ALSO independently verified on some other
// identity) must still count as exactly one match, never two.
func matchUserIDs(ctx context.Context, deps Deps, email string) ([]string, error) {
	seen := make(map[string]struct{}, 2)
	var out []string
	add := func(id pgtype.UUID) {
		s := id.String()
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}

	byPrimaryEmail, err := deps.Users.GetByPrimaryEmail(ctx, email)
	switch {
	case err == nil:
		add(byPrimaryEmail.ID)
	case errors.Is(err, pgx.ErrNoRows):
		// no primary_email match -- fine, the verified-identity-email
		// half below might still match.
	default:
		return nil, fmt.Errorf("get user by primary email: %w", err)
	}

	byVerifiedIdentity, err := deps.Identities.ListVerifiedUserIDsByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("list verified identity user ids by email: %w", err)
	}
	for _, id := range byVerifiedIdentity {
		add(id)
	}

	return out, nil
}

// autoLink inserts the identities row (linked_via=auto_email) and its
// audit-log entry in ONE transaction (§13.3: "written in the same
// transaction as the change"), then deletes any still-pending link
// prompt for this same identity -- a resolved auto-link supersedes an
// earlier "we couldn't tell yet" prompt.
//
// email_verified=true: unlike GitHub's /user/emails (githubUser's own doc
// comment, internal/adapters/inbound/auth/callback.go), Slack/Linear's own
// profile-email APIs carry no separate "verified" flag at all -- both
// require the REQUESTING workspace/app's own already-authenticated
// installation to read them in the first place (never attacker-supplied,
// unlike e.g. a user-entered email field), so this codebase treats a
// value fetched this way as verified for identities.email_verified's own
// purpose ("attested by the provider", not "independently re-verified by
// this codebase") -- the SAME standard GetByProviderAndExternalID's own
// GitHub-originated rows already apply.
//
// actor_user_id is NULL on the audit-log row: this is a SYSTEM-driven
// match (an automated algorithm resolved it), not a human clicking
// anything -- mirrors sessions.created_by/plans.decided_by's own
// established NULL-for-no-human-actor convention (§17.5), never a
// fabricated "system user" row; the matched user's own id is still
// recorded, in detail_json, for a reader of the audit log to see exactly
// who was linked.
func autoLink(ctx context.Context, deps Deps, provider sqlcgen.IdentityProvider, externalID, email, matchedUserIDStr string) (Resolution, error) {
	var matchedUserID pgtype.UUID
	if err := matchedUserID.Scan(matchedUserIDStr); err != nil {
		return Resolution{}, fmt.Errorf("identitylink: parse matched user id: %w", err)
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Resolution{}, fmt.Errorf("identitylink: begin auto-link tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := deps.Identities.WithTx(tx).Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:        matchedUserID,
		Provider:      provider,
		ExternalID:    externalID,
		Email:         &email,
		EmailVerified: true,
		LinkedVia:     sqlcgen.IdentityLinkedViaAutoEmail,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a race: a concurrent event for the SAME (provider,
			// externalID) already linked it (auto-link or an admin
			// force-link) between this call's own initial
			// GetByProviderAndExternalID miss and this INSERT. Resolve
			// the WINNER's real row instead of surfacing a spurious
			// error -- mirrors internal/adapters/inbound/slack's own
			// identical "lost the claim race, resolve the winner"
			// precedent (resolveOrClaimSession, handler.go).
			winner, getErr := deps.Identities.GetByProviderAndExternalID(ctx, provider, externalID)
			if getErr != nil {
				return Resolution{}, fmt.Errorf("identitylink: resolve winner after lost auto-link race: %w", getErr)
			}
			return Resolution{UserID: winner.UserID}, nil
		}
		return Resolution{}, fmt.Errorf("identitylink: create auto-linked identity: %w", err)
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), pgtype.UUID{}, "identity.auto_linked", "identity", created.ID.String(), map[string]any{
		"provider":    string(provider),
		"external_id": externalID,
		"user_id":     matchedUserIDStr,
	}); err != nil {
		return Resolution{}, fmt.Errorf("identitylink: record audit log: %w", err)
	}

	if err := deps.LinkPrompts.WithTx(tx).DeleteForProviderAndExternalID(ctx, provider, externalID); err != nil {
		return Resolution{}, fmt.Errorf("identitylink: delete superseded link prompts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Resolution{}, fmt.Errorf("identitylink: commit auto-link tx: %w", err)
	}

	return Resolution{UserID: matchedUserID, AutoLinked: true}, nil
}

// createOrReuseLinkPrompt implements §13.2 step 4's own "never guess:
// create a link prompt" branch, WITH one added policy this table's own
// migration explicitly leaves open ("no UNIQUE constraint... this table
// only provides the storage shape, not yet the concurrency/resend
// policy"): if a still-unexpired prompt already exists for this
// (provider, externalID), it is reused (MagicLinkURL left empty in the
// returned Resolution, since the caller has ALREADY posted that same link
// once and must not spam it again on every subsequent inbound message) --
// only once the latest one has expired (or none exists yet) is a fresh
// nonce minted and a NEW magic link returned for the caller to post.
func createOrReuseLinkPrompt(ctx context.Context, deps Deps, provider sqlcgen.IdentityProvider, externalID string) (Resolution, error) {
	latest, err := deps.LinkPrompts.GetLatestForProviderAndExternalID(ctx, provider, externalID)
	switch {
	case err == nil && latest.ExpiresAt.Valid && latest.ExpiresAt.Time.After(time.Now()):
		return Resolution{}, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return Resolution{}, fmt.Errorf("identitylink: look up latest link prompt: %w", err)
	}

	nonce, err := platform.GenerateToken()
	if err != nil {
		return Resolution{}, fmt.Errorf("identitylink: generate link prompt nonce: %w", err)
	}
	expiresAt := time.Now().Add(deps.PromptTTL)

	if _, err := deps.LinkPrompts.Create(ctx, sqlcgen.CreateIdentityLinkPromptParams{
		Provider:   provider,
		ExternalID: externalID,
		NonceHash:  platform.HashToken(nonce),
		ExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return Resolution{}, fmt.Errorf("identitylink: create link prompt: %w", err)
	}

	return Resolution{MagicLinkURL: BuildMagicLinkURL(deps.PublicBaseURL, nonce)}, nil
}

// Sentinel errors Consume returns -- distinct from a genuine (500-worthy)
// failure, so internal/adapters/inbound/identitylink's own handler can
// render the right user-facing outcome for each.
var (
	// ErrLinkPromptNotFound means the presented nonce matches no row --
	// wrong, already consumed, or never existed (this package does not
	// distinguish those, mirroring auth.Middleware's own identical
	// "no differential signal" precedent).
	ErrLinkPromptNotFound = errors.New("identitylink: link prompt not found")
	// ErrLinkPromptExpired means the row exists but IdentityLinkPromptTTL
	// has elapsed -- Consume deletes the stale row as a side effect of
	// detecting this (best-effort; a failure to delete does not change
	// this return value) so a later click of the SAME expired link
	// short-circuits to ErrLinkPromptNotFound instead next time.
	ErrLinkPromptExpired = errors.New("identitylink: link prompt expired")
	// ErrIdentityAlreadyLinked means (provider, externalID) got linked to
	// someone by a DIFFERENT path (a concurrent auto-link, or an admin
	// force-link) between this prompt being created and this click being
	// consumed.
	ErrIdentityAlreadyLinked = errors.New("identitylink: identity already linked")
)

// Consume implements the magic-link counterpart of §13.2 step 4: nonce is
// the PLAINTEXT value from the clicked URL (Consume hashes it itself,
// platform.HashToken, before looking anything up -- mirrors user_sessions/
// ws_tokens' own identical hash-at-rest convention); authenticatedUserID
// is the ALREADY-authenticated visiting user (internal/adapters/inbound/
// identitylink's own handler resolves this via the existing GitHub OAuth
// web-login cookie BEFORE ever calling Consume -- see that package's own
// doc comment).
//
// On success, inserts the identities row (linked_via=prompt) and its
// audit-log entry (actor_user_id = authenticatedUserID THIS time -- a
// real human explicitly completed this action by visiting their own
// confirmed link, unlike autoLink's own NULL-actor system-driven case
// above) in ONE transaction, then deletes EVERY pending prompt for that
// same (provider, externalID) so a stale link can never be replayed.
func Consume(ctx context.Context, deps Deps, nonce string, authenticatedUserID pgtype.UUID) (sqlcgen.Identity, error) {
	prompt, err := deps.LinkPrompts.GetByNonceHash(ctx, platform.HashToken(nonce))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.Identity{}, ErrLinkPromptNotFound
		}
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: look up link prompt: %w", err)
	}

	if !prompt.ExpiresAt.Valid || !prompt.ExpiresAt.Time.After(time.Now()) {
		// Best-effort cleanup -- its own failure must never mask the
		// real ErrLinkPromptExpired outcome below.
		_ = deps.LinkPrompts.Delete(ctx, prompt.ID)
		return sqlcgen.Identity{}, ErrLinkPromptExpired
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: begin consume tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := deps.Identities.WithTx(tx).Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:     authenticatedUserID,
		Provider:   prompt.Provider,
		ExternalID: prompt.ExternalID,
		LinkedVia:  sqlcgen.IdentityLinkedViaPrompt,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return sqlcgen.Identity{}, ErrIdentityAlreadyLinked
		}
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: create identity from link prompt: %w", err)
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), authenticatedUserID, "identity.linked_via_prompt", "identity", created.ID.String(), map[string]any{
		"provider":    string(prompt.Provider),
		"external_id": prompt.ExternalID,
	}); err != nil {
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: record audit log: %w", err)
	}

	if err := deps.LinkPrompts.WithTx(tx).DeleteForProviderAndExternalID(ctx, prompt.Provider, prompt.ExternalID); err != nil {
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: delete consumed link prompts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Identity{}, fmt.Errorf("identitylink: commit consume tx: %w", err)
	}

	return created, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) -- the one race both autoLink and Consume's
// own INSERT can lose to a concurrent winner targeting the SAME
// identities.(provider, external_id) unique key.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}
