// This file (uploadmint.go) implements the mint half of §8.6's upload
// lifecycle (§28.4/§28.5): POST .../uploads, in both auth variants. Mint
// declares {filename, contentType, sizeBytes}, checks it against
// MaxUploadBytes/MaxSessionUploadBytes (a fast-fail courtesy -- confirm.go
// re-checks both, at the authoritative moment), inserts the pending
// artifact row, and returns a presigned PUT URL.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/authz"
	domainupload "github.com/khazaddev/narvi/internal/domain/upload"
	"github.com/khazaddev/narvi/internal/platform"
)

// uploadMintRequest/uploadMintResponse are the sandbox-bearer mint
// variant's own plain, un-schema'd wire types -- mirroring
// scmCredentialsRequest/scmCredentialsResponse's own precedent
// (scmcredentials.go): every sandbox-bearer endpoint in this package uses
// a local Go struct, never a contracts/rest type, even where (as here)
// the JSON shape happens to coincide with a real contracts/rest
// definition (MintUploadRequest/MintUploadResponse, used by the browser
// variant below) -- contracts/rest is this codebase's browser-facing
// (/api) surface only, and keeping the two wire types independently
// declared (rather than sharing one Go type across both) means either
// surface can evolve its own shape later without touching the other.
type uploadMintRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type uploadMintResponse struct {
	UploadID  string            `json:"uploadId"`
	PutURL    string            `json:"putUrl"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expiresAt"`
}

// mintUploadResult is mintUploadCore's own return shape -- converted by
// each of this file's two handlers into ITS OWN wire response type
// (uploadMintResponse for sandbox-bearer, restdtos.MintUploadResponse for
// browser).
type mintUploadResult struct {
	UploadID  string
	PutURL    string
	Headers   map[string]string
	ExpiresAt string
}

// mintUploadCore is the shared logic both auth variants run after their
// own, DIFFERENT auth check passes (§28.5: two auth variants, one
// mechanism). createdBy.Valid means a real, authenticated browser caller;
// !createdBy.Valid means agent-produced (§17.5's own no-human-actor
// allowance, this table's existing convention for sessions.created_by).
//
// No explicit Postgres transaction wraps mint's own read-then-insert
// sequence (SumSessionUploadBytes, then CreateUpload): §28.4 is explicit
// that mint-time checks are a "fast-fail courtesy", never the enforcement
// of record -- two mints racing past the session cap is a KNOWN, ACCEPTED
// gap at this moment, closed instead at confirm (confirmUploadCore re-runs
// the identical check against each upload's own ACTUAL stat'd size,
// "re-checked now").
//
// Ordering matters: PresignPut runs BEFORE the row is ever inserted, so a
// (rare -- presigning is pure local signing, §28.1) presign failure never
// leaves a pending row with no way to ever be completed.
func mintUploadCore(ctx context.Context, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts, sessionID pgtype.UUID, createdBy pgtype.UUID, req uploadMintRequest) (mintUploadResult, *uploadError) {
	logger := platform.Logger(ctx)

	if objCfg == nil || blobStore == nil {
		return mintUploadResult{}, &uploadError{Status: http.StatusServiceUnavailable, Message: "uploads not configured"}
	}
	if req.Filename == "" {
		return mintUploadResult{}, &uploadError{Status: http.StatusBadRequest, Message: "filename is required"}
	}
	if req.SizeBytes <= 0 {
		return mintUploadResult{}, &uploadError{Status: http.StatusBadRequest, Message: "sizeBytes must be positive"}
	}
	// FIX A (security, layer 1 of 2 -- defense in depth): reject control
	// characters and an overlong value in EITHER field before a row is
	// ever created. This is the shared core BOTH auth variants run
	// through, so both get the identical check -- never duplicated
	// divergently between them. See domainupload.ValidateUploadMetadata's
	// own doc comment for the exact vulnerability this closes; prompt.go's
	// own sanitizeUntrustedField is the SECOND, independent layer, holding
	// even for a value that somehow bypasses this one.
	if err := domainupload.ValidateUploadMetadata(req.Filename, req.ContentType); err != nil {
		return mintUploadResult{}, &uploadError{Status: http.StatusBadRequest, Message: err.Error()}
	}

	sessionTotal, err := artifacts.SumSessionUploadBytes(ctx, sessionID)
	if err != nil {
		logger.Error("httpapi: sum session upload bytes at mint failed", "error", err)
		return mintUploadResult{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	if reason, ok := domainupload.EvaluateUploadSize(req.SizeBytes, objCfg.MaxUploadBytes, sessionTotal, objCfg.MaxSessionUploadBytes); !ok {
		return mintUploadResult{}, &uploadError{Status: http.StatusRequestEntityTooLarge, Message: mintLimitMessage(reason, objCfg)}
	}

	id := uuid.New()
	var artifactID pgtype.UUID
	if err := artifactID.Scan(id.String()); err != nil {
		logger.Error("httpapi: scan generated upload id failed", "error", err)
		return mintUploadResult{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	blobKey := domainupload.BuildBlobKey(sessionID.String(), id.String())

	presigned, err := blobStore.PresignPut(ctx, ports.PresignPutSpec{
		Key:           blobKey,
		ContentType:   req.ContentType,
		ContentLength: req.SizeBytes,
		TTL:           timeouts.UploadPresignPutTTL,
	})
	if err != nil {
		logger.Error("httpapi: presign put failed", "error", err)
		return mintUploadResult{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	contentURL := "/api/sessions/" + sessionID.String() + "/uploads/" + id.String() + "/content"
	blobKeyStr := string(blobKey)

	if _, err := artifacts.CreateUpload(ctx, sqlcgen.CreateUploadArtifactParams{
		ID:          artifactID,
		SessionID:   sessionID,
		Url:         contentURL,
		BlobKey:     &blobKeyStr,
		SizeBytes:   &req.SizeBytes,
		ContentType: &req.ContentType,
		Filename:    &req.Filename,
		CreatedBy:   createdBy,
	}); err != nil {
		logger.Error("httpapi: create upload artifact failed", "error", err)
		return mintUploadResult{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	return mintUploadResult{
		UploadID:  id.String(),
		PutURL:    presigned.URL,
		Headers:   presigned.Headers,
		ExpiresAt: presigned.ExpiresAt.Format(rfc3339Milli),
	}, nil
}

// rfc3339Milli matches every other timestamp this codebase already
// formats by hand for a JSON response carrying a plain string field
// (rather than a typed time.Time some other DTO field already handles via
// its own encoding/json time.Time support) -- mirrors ScmCredentialsResponse's
// own ExpiresAt time.Time field's default RFC3339Nano encoding closely
// enough that any reasonable RFC3339 parser on the consuming end handles
// either; kept as a named constant here rather than a magic string literal
// since uploadconfirm.go's own sibling response has no such field but a
// FUTURE one might.
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// mintLimitMessage renders a human-readable reason naming the exact limit
// an over-limit mint request violated (§28.4: "a structured 4xx naming the
// limit").
func mintLimitMessage(reason domainupload.FailureReason, objCfg *platform.ObjectStorageConfig) string {
	switch reason {
	case domainupload.FailureReasonSizeExceeded:
		return fmt.Sprintf("file exceeds the maximum upload size of %d bytes", objCfg.MaxUploadBytes)
	case domainupload.FailureReasonQuotaExceeded:
		return fmt.Sprintf("this session has reached its maximum total upload size of %d bytes", objCfg.MaxSessionUploadBytes)
	default:
		return "upload size limit exceeded"
	}
}

// MintUpload is the sandbox-bearer variant (§28.5): POST
// /sessions/{sessionID}/uploads, outside /api and outside auth.Middleware
// -- the agent-produced upload direction's own first call
// (RenderUploadToolNote's own rendered instructions). createdBy is left
// invalid (agent-produced, §17.5).
func MintUpload(sandboxes *postgres.SandboxStore, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sessionID pgtype.UUID
		if err := sessionID.Scan(chi.URLParam(r, "sessionID")); err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)

		if _, ok := sandboxBearerAuth(w, r, sandboxes, sessionID); !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req uploadMintRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		result, uerr := mintUploadCore(ctx, artifacts, blobStore, objCfg, timeouts, sessionID, pgtype.UUID{}, req)
		if uerr != nil {
			writeError(w, uerr.Status, uerr.Message)
			return
		}

		// uploadMintResponse's fields are identical in name/type/order to
		// mintUploadResult's (only JSON tags differ, which struct
		// conversion ignores) -- a direct conversion, not a coincidence:
		// see uploadMintResponse's own doc comment for why these stay two
		// independently-declared types despite that.
		writeJSON(w, http.StatusCreated, uploadMintResponse(result))
	}
}

// MintUploadAPI is the browser variant (§28.5): POST
// /api/sessions/:id/uploads, inside /api and auth.Middleware, gated by
// authz.ActionUploadToSession -- the SAME §13.3 row as prompting
// (member+, own/joined; viewer never uploads).
func MintUploadAPI(sessions *postgres.SessionStore, participants *postgres.ParticipantStore, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		if !authorizeUploadToSession(w, r, sessions, participants, sessionID, actorUserID) {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.MintUploadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		result, uerr := mintUploadCore(ctx, artifacts, blobStore, objCfg, timeouts, sessionID, actorUserID, uploadMintRequest{
			Filename:    req.Filename,
			ContentType: req.ContentType,
			SizeBytes:   int64(req.SizeBytes),
		})
		if uerr != nil {
			logger.Error("httpapi: mint upload failed", "status", uerr.Status, "message", uerr.Message)
			writeError(w, uerr.Status, uerr.Message)
			return
		}

		expiresAt, err := time.Parse(rfc3339Milli, result.ExpiresAt)
		if err != nil {
			logger.Error("httpapi: parse mint expiresAt failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.MintUploadResponse{
			UploadId:  result.UploadID,
			PutUrl:    result.PutURL,
			Headers:   result.Headers,
			ExpiresAt: expiresAt,
		})
	}
}

// authorizeUploadToSession is MintUploadAPI/ConfirmUploadAPI's own shared
// ownership-then-authorize check -- mirrors CreateTurn's own identical
// inline sequence (turn.go) exactly (§28.5: "the same §13.3 row as
// prompting"), factored out here since both this file and
// uploadconfirm.go need the identical check.
func authorizeUploadToSession(w http.ResponseWriter, r *http.Request, sessions *postgres.SessionStore, participants *postgres.ParticipantStore, sessionID, actorUserID pgtype.UUID) bool {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	sessionRow, err := sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return false
		}
		logger.Error("httpapi: get session for upload authorization failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}

	ownedOrJoined := sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID
	if !ownedOrJoined {
		exists, err := participants.Exists(ctx, sessionRow.ID, actorUserID)
		if err != nil {
			logger.Error("httpapi: check participant for upload authorization failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return false
		}
		ownedOrJoined = exists
	}
	return authorize(w, r, authz.ActionUploadToSession, authz.Resource{OwnedOrJoined: ownedOrJoined})
}
