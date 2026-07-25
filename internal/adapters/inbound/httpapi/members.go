// This file (members.go) implements Step 39's ("identities + full RBAC")
// own second deliverable: the backend-only members API (§13.2/§13.3) --
// list members (with role, linked identities, and system-wide pending
// link-prompt state), an admin-only role-change endpoint, admin manual
// link/unlink of an identity (§13.2 point 5, "admin can force-link"), and
// a read endpoint over audit_log. The actual Settings -> Members UI is
// Phase 7 (§13.4) and explicitly out of scope here -- these are plain
// REST endpoints for whatever future Step builds that screen, following
// this package's own existing conventions (parseSessionID-style path-
// param parsing, writeError/writeJSON, the authorize() helper) rather
// than inventing new ones.
//
// Every handler below is gated by domain/authz.Authorize(actor,
// authz.ActionManageMembers, ...) -- §13.3's own matrix bundles "members &
// roles" as ONE admin-only row (no separate read-vs-write split named in
// the spec table itself), so every route here, including the two plain
// read endpoints (ListMembers, ListAuditLog), reuses that SAME action
// rather than inventing finer-grained Actions the spec doesn't call for.
//
// Routes (mounted by cmd/control-plane/main.go, behind auth.Middleware,
// like every other route in this package):
//
//   - GET    /api/members                              -- ListMembers
//   - PATCH  /api/members/{userID}/role                 -- UpdateMemberRole
//   - POST   /api/members/{userID}/identities            -- LinkMemberIdentity
//   - DELETE /api/members/{userID}/identities/{identityID} -- UnlinkMemberIdentity
//   - GET    /api/audit-log                              -- ListAuditLog

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// uniqueViolationCode is Postgres' own SQLSTATE for a unique-constraint
// violation -- mirrors internal/app/identitylink/service.go's own
// identical constant/isUniqueViolation pair (that package's own doc
// comment on isUniqueViolation explains the general pattern; not reused
// directly from there since it's unexported and this is a different
// package). LinkMemberIdentity's own race fix below is this file's one
// use of it.
const uniqueViolationCode = "23505"

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// identityDTO is one linked-identity row's own REST wire shape.
type identityDTO struct {
	ID         string    `json:"id"`
	Provider   string    `json:"provider"`
	ExternalID string    `json:"externalId"`
	LinkedVia  string    `json:"linkedVia"`
	CreatedAt  time.Time `json:"createdAt"`
}

func identityToDTO(i sqlcgen.Identity) identityDTO {
	return identityDTO{
		ID:         i.ID.String(),
		Provider:   string(i.Provider),
		ExternalID: i.ExternalID,
		LinkedVia:  string(i.LinkedVia),
		CreatedAt:  i.CreatedAt.Time,
	}
}

// memberDTO is one member's own REST wire shape -- role + every identity
// currently linked to them (§13.3: "linked identity chips").
type memberDTO struct {
	ID          string        `json:"id"`
	Email       string        `json:"email"`
	DisplayName string        `json:"displayName"`
	Role        string        `json:"role"`
	Disabled    bool          `json:"disabled"`
	CreatedAt   time.Time     `json:"createdAt"`
	Identities  []identityDTO `json:"identities"`
}

// pendingLinkPromptDTO is one still-present identity_link_prompts row's
// own REST wire shape -- deliberately carries NO nonce/nonce hash (a
// bearer secret, never surfaced over this read endpoint) -- just enough
// for an admin-facing view to show "someone from Slack/Linear is waiting
// to connect their account" (§13.2's own "pending-link state").
type pendingLinkPromptDTO struct {
	Provider   string    `json:"provider"`
	ExternalID string    `json:"externalId"`
	ExpiresAt  time.Time `json:"expiresAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

// listMembersResponse is GET /api/members's own response body.
type listMembersResponse struct {
	Members            []memberDTO            `json:"members"`
	PendingLinkPrompts []pendingLinkPromptDTO `json:"pendingLinkPrompts"`
}

// ListMembers backs GET /api/members: admin-only (authz.ActionManageMembers),
// returns every user with role/disabled and their own currently-linked
// identities, plus every system-wide still-pending link prompt (§13.2's
// own "pending-link state" -- not yet associated with any member, by
// definition, since resolving THAT is exactly what a prompt is still
// waiting on).
func ListMembers(users *postgres.UserStore, identities *postgres.IdentityStore, linkPrompts *postgres.IdentityLinkPromptStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageMembers, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userRows, err := users.List(ctx)
		if err != nil {
			logger.Error("httpapi: list members: list users failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		memberDTOs := make([]memberDTO, 0, len(userRows))
		for _, u := range userRows {
			identityRows, err := identities.ListForUser(ctx, u.ID)
			if err != nil {
				logger.Error("httpapi: list members: list identities failed", "error", err, "user_id", u.ID.String())
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			identityDTOs := make([]identityDTO, 0, len(identityRows))
			for _, i := range identityRows {
				identityDTOs = append(identityDTOs, identityToDTO(i))
			}
			memberDTOs = append(memberDTOs, memberDTO{
				ID:          u.ID.String(),
				Email:       u.PrimaryEmail,
				DisplayName: u.DisplayName,
				Role:        string(u.Role),
				Disabled:    u.Disabled,
				CreatedAt:   u.CreatedAt.Time,
				Identities:  identityDTOs,
			})
		}

		promptRows, err := linkPrompts.List(ctx)
		if err != nil {
			logger.Error("httpapi: list members: list link prompts failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		promptDTOs := make([]pendingLinkPromptDTO, 0, len(promptRows))
		for _, p := range promptRows {
			promptDTOs = append(promptDTOs, pendingLinkPromptDTO{
				Provider:   string(p.Provider),
				ExternalID: p.ExternalID,
				ExpiresAt:  p.ExpiresAt.Time,
				CreatedAt:  p.CreatedAt.Time,
			})
		}

		writeJSON(w, http.StatusOK, listMembersResponse{Members: memberDTOs, PendingLinkPrompts: promptDTOs})
	}
}

// updateMemberRoleRequest is PATCH /api/members/{userID}/role's own
// request body.
type updateMemberRoleRequest struct {
	Role string `json:"role"`
}

// validRoles is the closed set of role strings UpdateMemberRole accepts --
// mirrors authz.AllRoles exactly (converted to plain strings once here,
// rather than re-deriving it per request).
var validRoles = map[string]sqlcgen.UserRole{
	string(authz.RoleAdmin):      sqlcgen.UserRoleAdmin,
	string(authz.RoleMaintainer): sqlcgen.UserRoleMaintainer,
	string(authz.RoleMember):     sqlcgen.UserRoleMember,
	string(authz.RoleViewer):     sqlcgen.UserRoleViewer,
}

// UpdateMemberRole backs PATCH /api/members/{userID}/role: admin-only
// (authz.ActionManageMembers, §13.3's own "members & roles: admin only"
// row), body {"role": "admin"|"maintainer"|"member"|"viewer"}. 400 on a
// malformed body or an unrecognized role string; 404 if userID doesn't
// name a real user; 409 if the target is CURRENTLY an active (non-
// disabled) admin and this change would leave zero active admins left
// (the last-admin guard, an audit finding: H8 -- demoting the sole
// remaining admin would permanently lock the deployment out of every
// admin-only endpoint, including this one, with no recovery path short
// of direct DB surgery); else 200 with the updated memberDTO (identities
// omitted -- this endpoint changes a role, not the identity graph; a
// caller that also wants the identity list calls ListMembers).
func UpdateMemberRole(pool *pgxpool.Pool, users *postgres.UserStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageMembers, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		targetUserID, ok := parseUUIDParam(w, r, "userID", "malformed member id")
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req updateMemberRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		newRole, ok := validRoles[req.Role]
		if !ok {
			writeError(w, http.StatusBadRequest, "unrecognized role")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: update member role: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		existing, err := users.WithTx(tx).GetByID(ctx, targetUserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "member not found")
				return
			}
			logger.Error("httpapi: update member role: get user failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Last-admin guard (audit finding H8): only relevant when this
		// change actually TAKES admin away from someone who currently HAS
		// it (a disabled admin, or a no-op admin->admin "change", never
		// counted as active in the first place, so neither can trip this).
		if existing.Role == sqlcgen.UserRoleAdmin && !existing.Disabled && newRole != sqlcgen.UserRoleAdmin {
			activeAdminIDs, err := users.WithTx(tx).ListActiveAdminIDsForUpdate(ctx)
			if err != nil {
				logger.Error("httpapi: update member role: list active admins failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			// existing is itself one of the rows ListActiveAdminIDsForUpdate
			// just locked (its own role/disabled columns, read a moment ago
			// in this SAME transaction, already satisfy that query's own
			// WHERE clause) -- so len<=1 here can only mean targetUserID IS
			// that one remaining active admin.
			if len(activeAdminIDs) <= 1 {
				writeError(w, http.StatusConflict, "cannot demote the last remaining admin")
				return
			}
		}

		updated, err := users.WithTx(tx).UpdateRole(ctx, targetUserID, newRole)
		if err != nil {
			logger.Error("httpapi: update member role: update failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := auditlog.Record(ctx, auditLog.WithTx(tx), actorUserID, "member.role_changed", "user", targetUserID.String(), map[string]any{
			"from_role": string(existing.Role),
			"to_role":   string(updated.Role),
		}); err != nil {
			logger.Error("httpapi: update member role: record audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: update member role: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, memberDTO{
			ID:          updated.ID.String(),
			Email:       updated.PrimaryEmail,
			DisplayName: updated.DisplayName,
			Role:        string(updated.Role),
			Disabled:    updated.Disabled,
			CreatedAt:   updated.CreatedAt.Time,
		})
	}
}

// linkMemberIdentityRequest is POST /api/members/{userID}/identities's own
// request body.
type linkMemberIdentityRequest struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"externalId"`
}

// validProviders is the closed set of provider strings LinkMemberIdentity
// accepts -- mirrors the identity_provider Postgres enum
// (migrations/000003_identities.up.sql) exactly.
var validProviders = map[string]sqlcgen.IdentityProvider{
	string(sqlcgen.IdentityProviderGithub): sqlcgen.IdentityProviderGithub,
	string(sqlcgen.IdentityProviderSlack):  sqlcgen.IdentityProviderSlack,
	string(sqlcgen.IdentityProviderLinear): sqlcgen.IdentityProviderLinear,
	string(sqlcgen.IdentityProviderGoogle): sqlcgen.IdentityProviderGoogle,
}

// LinkMemberIdentity backs POST /api/members/{userID}/identities:
// admin-only (authz.ActionManageMembers), body {"provider":...,
// "externalId":...} -- §13.2 point 5's own "admin can force-link".
// linked_via is always "admin" (sqlcgen.IdentityLinkedViaAdmin) here,
// regardless of what created the row this endpoint is being used to
// correct/establish -- an admin explicitly choosing to link two specific
// ids together, through this endpoint, is exactly what that enum value
// means (mirrors internal/adapters/inbound/auth's own identical
// first-time-sign-in overload of the same value, see that package's own
// createUserAndIdentity doc comment).
//
// 404 if userID doesn't name a real user. 409 if (provider, externalId)
// is ALREADY linked to a DIFFERENT user (an admin must unlink it first,
// via UnlinkMemberIdentity, rather than this endpoint silently
// reassigning it). 200 (not 201) if it's already linked to THIS SAME
// user -- idempotent, not an error. Else 201 with the new identityDTO.
//
// The already-linked check (identities.GetByProviderAndExternalID) and
// the subsequent Create both run INSIDE the same transaction (an audit
// finding, H8: they used to straddle two separate transactions/
// connections, so a concurrent duplicate-link request could race past
// the check and hit the identities.(provider, external_id) unique
// constraint directly, surfacing as a raw 500 instead of this handler's
// own intended 409/200). Running the check inside the transaction
// narrows that window; the Create call's own isUniqueViolation branch
// below closes it completely -- mirrors internal/app/identitylink's own
// autoLink "lost the race, resolve the winner" precedent (service.go).
func LinkMemberIdentity(pool *pgxpool.Pool, users *postgres.UserStore, identities *postgres.IdentityStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageMembers, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		targetUserID, ok := parseUUIDParam(w, r, "userID", "malformed member id")
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req linkMemberIdentityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		provider, ok := validProviders[req.Provider]
		if !ok {
			writeError(w, http.StatusBadRequest, "unrecognized provider")
			return
		}
		if req.ExternalID == "" {
			writeError(w, http.StatusBadRequest, "externalId is required")
			return
		}

		if _, err := users.GetByID(ctx, targetUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "member not found")
				return
			}
			logger.Error("httpapi: link member identity: get user failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: link member identity: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		existing, err := identities.WithTx(tx).GetByProviderAndExternalID(ctx, provider, req.ExternalID)
		if err == nil {
			if existing.UserID != targetUserID {
				writeError(w, http.StatusConflict, "this identity is already linked to a different member")
				return
			}
			writeJSON(w, http.StatusOK, identityToDTO(existing))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Error("httpapi: link member identity: lookup existing identity failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		created, err := identities.WithTx(tx).Create(ctx, sqlcgen.CreateIdentityParams{
			UserID:     targetUserID,
			Provider:   provider,
			ExternalID: req.ExternalID,
			LinkedVia:  sqlcgen.IdentityLinkedViaAdmin,
		})
		if err != nil {
			if isUniqueViolation(err) {
				// Lost the race: a concurrent LinkMemberIdentity call for
				// the SAME (provider, externalId) committed its own Create
				// between our GetByProviderAndExternalID check above and
				// this INSERT. This tx is now aborted (Postgres refuses
				// further commands on it after a constraint violation), so
				// resolve the winner via a fresh, pool-scoped lookup --
				// mirrors internal/app/identitylink's own autoLink "lost
				// the race, resolve the winner" precedent (service.go) --
				// and answer with the exact same 409-vs-200 split the
				// check above would have given had it merely run a moment
				// later.
				winner, getErr := identities.GetByProviderAndExternalID(ctx, provider, req.ExternalID)
				if getErr != nil {
					logger.Error("httpapi: link member identity: resolve winner after lost race failed", "error", getErr)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				if winner.UserID != targetUserID {
					writeError(w, http.StatusConflict, "this identity is already linked to a different member")
					return
				}
				writeJSON(w, http.StatusOK, identityToDTO(winner))
				return
			}
			logger.Error("httpapi: link member identity: create failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := auditlog.Record(ctx, auditLog.WithTx(tx), actorUserID, "identity.force_linked", "identity", created.ID.String(), map[string]any{
			"user_id":     targetUserID.String(),
			"provider":    string(provider),
			"external_id": req.ExternalID,
		}); err != nil {
			logger.Error("httpapi: link member identity: record audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: link member identity: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusCreated, identityToDTO(created))
	}
}

// UnlinkMemberIdentity backs DELETE
// /api/members/{userID}/identities/{identityID}: admin-only
// (authz.ActionManageMembers). 404 (never distinguished from a genuine
// "no such identity at all") if identityID doesn't exist OR exists but
// belongs to a DIFFERENT user than userID -- mirrors auth.Middleware's own
// "no differential signal" precedent: a caller probing identity ids
// belonging to other members learns nothing extra from the response.
// Else 204, with an audit-log entry recorded first, in the SAME
// transaction as the delete.
func UnlinkMemberIdentity(pool *pgxpool.Pool, identities *postgres.IdentityStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageMembers, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		targetUserID, ok := parseUUIDParam(w, r, "userID", "malformed member id")
		if !ok {
			return
		}
		identityID, ok := parseUUIDParam(w, r, "identityID", "malformed identity id")
		if !ok {
			return
		}

		existing, err := identities.ListForUser(ctx, targetUserID)
		if err != nil {
			logger.Error("httpapi: unlink member identity: list identities failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		var target sqlcgen.Identity
		found := false
		for _, i := range existing {
			if i.ID == identityID {
				target = i
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "identity not found")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: unlink member identity: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := auditlog.Record(ctx, auditLog.WithTx(tx), actorUserID, "identity.unlinked", "identity", identityID.String(), map[string]any{
			"user_id":     targetUserID.String(),
			"provider":    string(target.Provider),
			"external_id": target.ExternalID,
		}); err != nil {
			logger.Error("httpapi: unlink member identity: record audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if _, err := identities.WithTx(tx).Delete(ctx, identityID); err != nil {
			logger.Error("httpapi: unlink member identity: delete failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: unlink member identity: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// auditLogEntryDTO is one audit_log row's own REST wire shape.
type auditLogEntryDTO struct {
	ID            string          `json:"id"`
	ActorUserID   *string         `json:"actorUserId"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resourceType"`
	ResourceID    string          `json:"resourceId"`
	Detail        json.RawMessage `json:"detail"`
	CorrelationID *string         `json:"correlationId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// listAuditLogResponse is GET /api/audit-log's own response body.
type listAuditLogResponse struct {
	Entries []auditLogEntryDTO `json:"entries"`
}

// defaultAuditLogPageSize/maxAuditLogPageSize bound GET /api/audit-log's
// own ?limit= query param -- a plain, small page size (see queries/
// audit_log.sql's own doc comment on ListAuditLogEntries for why this is
// deliberately NOT cursor-paginated).
const (
	defaultAuditLogPageSize = 50
	maxAuditLogPageSize     = 200
)

// ListAuditLog backs GET /api/audit-log?limit=&offset=: admin-only
// (authz.ActionManageMembers, §13.3: "surfaced in Settings -> Members
// ('Audit log')"). limit defaults to defaultAuditLogPageSize, capped at
// maxAuditLogPageSize; offset defaults to 0. Malformed limit/offset query
// values are treated as absent (defaulted), never a 400 -- this is a
// read-only convenience endpoint for an admin-facing view, not a strict
// API contract a machine client depends on getting a validation error
// from.
func ListAuditLog(auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageMembers, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		limit := int32(defaultAuditLogPageSize)
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= maxAuditLogPageSize {
				limit = int32(parsed)
			}
		}
		offset := int32(0)
		if raw := r.URL.Query().Get("offset"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
				offset = int32(parsed)
			}
		}

		rows, err := auditLog.List(ctx, limit, offset)
		if err != nil {
			logger.Error("httpapi: list audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		entries := make([]auditLogEntryDTO, 0, len(rows))
		for _, row := range rows {
			var actorUserID *string
			if row.ActorUserID.Valid {
				s := row.ActorUserID.String()
				actorUserID = &s
			}
			entries = append(entries, auditLogEntryDTO{
				ID:            row.ID.String(),
				ActorUserID:   actorUserID,
				Action:        row.Action,
				ResourceType:  row.ResourceType,
				ResourceID:    row.ResourceID,
				Detail:        json.RawMessage(row.DetailJson),
				CorrelationID: row.CorrelationID,
				CreatedAt:     row.CreatedAt.Time,
			})
		}

		writeJSON(w, http.StatusOK, listAuditLogResponse{Entries: entries})
	}
}

// parseUUIDParam parses chi's own paramName URL path param as a UUID,
// writing a 400 response and returning ok=false on failure -- mirrors
// parseSessionID's own identical shape, generalized to an arbitrary
// param name since this file's own routes carry two different id kinds
// (userID, identityID) neither of which is a sessionID.
func parseUUIDParam(w http.ResponseWriter, r *http.Request, paramName, errMessage string) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, paramName)
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, errMessage)
		return pgtype.UUID{}, false
	}
	return id, true
}
