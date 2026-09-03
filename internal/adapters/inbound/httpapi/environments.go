// This file (environments.go) implements the first standalone READ over
// the environments table (§14.1, migrations/000021_environments.up.sql):
// GET /api/environments. environment_store.go's own doc comment has been
// explicit since that migration that create/update stay inline, at
// session-creation time only (httpapi.CreateSession) -- extending that
// into a full CRUD surface (a name, an ordered repo list, image-build
// tracking) is a materially larger data-model change than this handler
// makes, and is declined here (see docs/design/mockups.html's own
// "Environments" cards for what that richer surface would need, none of
// which exists on this row today).
//
// What THIS handler closes is a narrower, real gap: every
// environment-scoped §27 sub-resource this codebase already ships
// (sandbox-secrets, opencode-config, cloud-identity-bindings,
// cluster-binding -- all keyed by environments.id) had no way for a
// caller to ever discover a valid id to manage, short of already knowing
// one from a session. Gated by the existing authz.ActionManageEnvironments
// (§13.3 row 4, maintainer+) -- reserved since migrations/
// 000021_environments.up.sql's own era specifically for "environments
// management UI is Phase 7" (internal/domain/authz/action.go's own doc
// comment), this is that UI's first caller. One action gates the whole
// read+write surface here, mirroring ActionManageFalsePositivePatterns'
// own "one action gates every endpoint of this lifecycle-management
// surface, including reads" precedent (falsepositivepatterns.go).

package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// environmentToDTO converts one sqlcgen.Environment row into its REST wire
// shape. No secret material on this row at all (§27.1/§27.2 secrets and
// config live in sibling tables, keyed by this row's own id) -- every
// field here is returned in full, never masked.
func environmentToDTO(row sqlcgen.Environment) restdtos.Environment {
	dto := restdtos.Environment{
		Id:             row.ID.String(),
		MockConfigured: row.MockConfigured,
		DockerRequired: row.DockerRequired,
		CreatedAt:      row.CreatedAt.Time,
		ContractsPath:  restdtos.EnvironmentContractsPath(row.ContractsPath),
	}
	if len(row.PathScope) > 0 {
		var patterns []string
		if err := json.Unmarshal(row.PathScope, &patterns); err == nil {
			ps := restdtos.EnvironmentPathScope(patterns)
			dto.PathScope = &ps
		}
	}
	if row.EgressPolicyMode != nil {
		mode := restdtos.EnvironmentEgressPolicyMode{Value: *row.EgressPolicyMode}
		dto.EgressPolicyMode = &mode
	}
	if len(row.EgressPolicyAllowlist) > 0 {
		var hosts []string
		if err := json.Unmarshal(row.EgressPolicyAllowlist, &hosts); err == nil {
			al := restdtos.EnvironmentEgressPolicyAllowlist(hosts)
			dto.EgressPolicyAllowlist = &al
		}
	}
	return dto
}

// ListEnvironments backs GET /api/environments: 403 if the caller fails
// authz.ActionManageEnvironments; 200 with restdtos.ListEnvironmentsResponse
// otherwise, newest-first (environment_store.go's own List doc comment).
func ListEnvironments(environments *postgres.EnvironmentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageEnvironments, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		rows, err := environments.List(ctx)
		if err != nil {
			logger.Error("httpapi: list environments failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.Environment, len(rows))
		for i, row := range rows {
			wire[i] = environmentToDTO(row)
		}
		writeJSON(w, http.StatusOK, restdtos.ListEnvironmentsResponse{Environments: wire})
	}
}
