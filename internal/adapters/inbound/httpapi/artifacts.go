package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
)

// ListArtifacts backs GET /api/sessions/{sessionID}/artifacts (§6.3).
// Session existence is checked first -- 404 if it doesn't exist;
// otherwise every artifact row (unbounded -- expected to stay small, per
// ArtifactStore.ListForSession's own doc comment) as
// restdtos.ArtifactsResponse. Nothing in this codebase mints an artifact
// row yet (see internal/adapters/outbound/postgres/artifact_store.go's
// own doc comment) -- this endpoint's happy-path response is an empty
// array until a later Step (PR creation §9.3+, previews §8.2,
// uploads §8.6) starts producing rows.
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
	// status/failureReason (§28.6) are additive fields on the
	// wire ArtifactsResponse shape, mirroring the sandbox-ws artifact
	// event's own identical additive change and wshub's own
	// artifactWireMap twin -- always present here (never omitted), since
	// every row already has a real status by migration 000060's own
	// DEFAULT 'ready' backfill.
	var failureReason interface{}
	if a.FailureReason != nil {
		failureReason = *a.FailureReason
	}
	// filename/sizeBytes/contentType (§12.2 item 1's own rail/composer
	// consumer, §28): sqlcgen.Artifact has carried these three columns since
	// migration 000060 (uploadmint.go's own CreateUpload call already
	// populates them on every upload row), but this wire map never
	// surfaced them -- a gap invisible until now because nothing
	// client-side ever read GET .../artifacts before this Step. All three
	// are nil for a pr/preview row (only an upload row ever sets them),
	// so each maps to a null property here, exactly like FailureReason
	// immediately above -- never an empty string standing in for
	// "unknown", which a filename-display caller could mistake for a
	// real (if empty) filename.
	var filename, contentType interface{}
	if a.Filename != nil {
		filename = *a.Filename
	}
	if a.ContentType != nil {
		contentType = *a.ContentType
	}
	var sizeBytes interface{}
	if a.SizeBytes != nil {
		sizeBytes = *a.SizeBytes
	}
	return map[string]interface{}{
		"id":            a.ID,
		"type":          a.Type,
		"url":           a.Url,
		"metadata":      json.RawMessage(a.Metadata),
		"createdAt":     a.CreatedAt,
		"status":        a.Status,
		"failureReason": failureReason,
		"filename":      filename,
		"sizeBytes":     sizeBytes,
		"contentType":   contentType,
	}
}
