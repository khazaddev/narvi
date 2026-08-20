package httpapi

import (
	"net/http"
	"strconv"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// listSessionsDefaultLimit / listSessionsMaxLimit bound ?limit= on
// ListSessions below -- mirrors ListEvents' own default/max convention
// one file over (events.go), though this endpoint has no existing sibling
// to match exactly since no session-list route existed before this Step
// (§12.2 item 1). No cursor pagination in this first cut -- see
// ListSessionsResponse's own schema doc comment (contracts/rest/v1/
// dtos.schema.json) for why: the sidebar this backs is not expected to
// need deep pagination yet, matching ArtifactsResponse/ListPlansResponse's
// own identical "expected to stay small, deepen later if that stops being
// true" precedent.
const (
	listSessionsDefaultLimit = 50
	listSessionsMaxLimit     = 200
)

// ListSessions backs GET /api/sessions?filter=mine|all&limit= (§6.3/§12.2
// item 1's own sidebar addition). filter defaults to "mine" --
// §12.2 item 1's own "'My sessions' = created or joined" definition,
// implemented by SessionStore.List's own mine_only join (see
// ListSessions' generated sqlc doc comment, sessions.sql.go, for the
// exact created_by-OR-participant logic). filter=all returns every
// unarchived session system-wide -- there is no per-session RBAC/
// visibility concept in this codebase today (httpapi/doc.go's own "every
// route ... 401 before reaching any handler, nothing narrower"), so this
// is a plain client-requested view, not a privilege escalation; any
// value other than "mine"/"all" is rejected with 400 rather than silently
// falling back to one or the other.
func ListSessions(sessions *postgres.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		mineOnly := true
		if raw := r.URL.Query().Get("filter"); raw != "" {
			switch raw {
			case "mine":
				mineOnly = true
			case "all":
				mineOnly = false
			default:
				writeError(w, http.StatusBadRequest, "filter must be \"mine\" or \"all\"")
				return
			}
		}

		limit := listSessionsDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, "malformed limit")
				return
			}
			limit = parsed
			if limit > listSessionsMaxLimit {
				limit = listSessionsMaxLimit
			}
		}

		rows, err := sessions.List(ctx, sqlcgen.ListSessionsParams{
			MineOnly: mineOnly,
			UserID:   userID,
			RowLimit: int32(limit),
		})
		if err != nil {
			logger.Error("httpapi: list sessions failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.Session, len(rows))
		for i, row := range rows {
			wire[i] = sessionRowToDTO(row.Session, row.SandboxStatus)
		}

		writeJSON(w, http.StatusOK, restdtos.ListSessionsResponse{Sessions: wire})
	}
}
