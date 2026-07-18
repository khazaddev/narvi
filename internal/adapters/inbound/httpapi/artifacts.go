package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// ListArtifacts backs GET /api/sessions/{sessionID}/artifacts (§6.3).
// Session existence is checked first -- 404 if it doesn't exist;
// otherwise every artifact row (unbounded -- expected to stay small, per
// ArtifactStore.ListForSession's own doc comment) as
// restdtos.ArtifactsResponse. Nothing in this codebase mints an artifact
// row yet (see internal/adapters/outbound/postgres/artifact_store.go's
// own doc comment) -- this endpoint's happy-path response is an empty
// array until a later Step (PR creation Step 21+, previews Step 48,
// uploads Step 49) starts producing rows.
func ListArtifacts(sessions *postgres.SessionStore, artifacts *postgres.ArtifactStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		logger := platform.Logger(ctx)

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		rows, err := artifacts.ListForSession(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: list artifacts failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.ArtifactsResponseArtifactsElem, len(rows))
		for i, a := range rows {
			wire[i] = artifactWireMap(a)
		}

		writeJSON(w, http.StatusOK, restdtos.ArtifactsResponse{Artifacts: wire})
	}
}

// artifactWireMap mirrors eventWireMap's own reasoning (events.go) --
// sqlcgen.Artifact.Metadata is likewise a plain []byte needing the same
// json.RawMessage treatment to avoid base64-encoding.
func artifactWireMap(a sqlcgen.Artifact) map[string]interface{} {
	return map[string]interface{}{
		"id":        a.ID,
		"type":      a.Type,
		"url":       a.Url,
		"metadata":  json.RawMessage(a.Metadata),
		"createdAt": a.CreatedAt,
	}
}
