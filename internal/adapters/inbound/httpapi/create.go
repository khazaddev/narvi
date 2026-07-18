package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// CreateSession backs POST /api/sessions (§6.3). Decodes
// restdtos.CreateSessionRequest from a body bounded by
// http.MaxBytesReader(maxRequestBodyBytes) -- an oversized body surfaces
// as *http.MaxBytesError, reported as 413; any other decode failure
// (malformed JSON) is 400. repos' own schema-level minItems:1 is not
// enforced by Go's plain json.Unmarshal, so it is checked explicitly here
// -- 400 on an empty list. The session is inserted with created_by: NULL
// always (see doc.go's own auth-gap writeup); CreateSessionRequest.prompt
// is decoded but deliberately NOT acted upon (dispatching a first turn
// needs a real sandbox spawn, Step 21's job), and repos itself is
// validated but not persisted anywhere (no repos column exists on
// sessions -- SESSION_CONFIG assembly is a later Step's own job).
// Responds 201 with the created row as restdtos.Session.
func CreateSession(sessions *postgres.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

		var req restdtos.CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		if len(req.Repos) < 1 {
			writeError(w, http.StatusBadRequest, "repos must be non-empty")
			return
		}

		created, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{
			Title:       req.Title,
			SpawnSource: sqlcgen.SessionSpawnSource(req.SpawnSource),
			CreatedBy:   pgtype.UUID{}, // NULL -- see doc.go's own auth-gap writeup.
		})
		if err != nil {
			logger.Error("httpapi: create session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusCreated, sessionToDTO(created))
	}
}
