// This file (uploadcontent.go) implements the download_file tool's own
// redirect endpoint (§28.5): GET .../uploads/{uploadId}/content, in both
// auth variants. A single 302 to a presigned GET makes the whole tool one
// command -- curl does not forward Authorization across a cross-host
// redirect by default, so the storage endpoint never sees the sandbox
// bearer, and the presigned URL itself never appears in any prompt,
// transcript, or persisted event (it exists only inside curl's own
// redirect-follow). response-content-disposition forces `attachment` so
// user-supplied content is never rendered inline off the storage origin.

package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// uploadContentCore is the shared logic both auth variants run after
// their own, DIFFERENT auth check passes: sandbox-bearer (dead-sandbox/
// gen/token) for the in-sandbox agent's own download_file calls, session
// visibility (read) for the browser -- "download is a read, so read-only
// viewers may" (§28.5).
func uploadContentCore(ctx context.Context, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts, sessionID, uploadID pgtype.UUID) (ports.PresignedURL, *uploadError) {
	logger := platform.Logger(ctx)

	row, err := artifacts.GetForSession(ctx, uploadID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.PresignedURL{}, &uploadError{Status: http.StatusNotFound, Message: "upload not found"}
		}
		logger.Error("httpapi: get artifact for content failed", "error", err)
		return ports.PresignedURL{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}
	// §28.5: "404 uploadID unknown, not this session's, or not
	// status='ready'" -- all three collapse to the same 404, never
	// distinguished for the caller.
	if row.Type != sqlcgen.ArtifactTypeUpload || row.Status != sqlcgen.ArtifactStatusReady {
		return ports.PresignedURL{}, &uploadError{Status: http.StatusNotFound, Message: "upload not found"}
	}

	if objCfg == nil || blobStore == nil {
		logger.Error("httpapi: content requested for a ready upload but object storage is not configured")
		return ports.PresignedURL{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	presigned, err := blobStore.PresignGet(ctx, ports.PresignGetSpec{
		Key:              ports.BlobKey(stringOrEmpty(row.BlobKey)),
		TTL:              timeouts.UploadPresignGetTTL,
		ResponseFilename: stringOrEmpty(row.Filename),
	})
	if err != nil {
		logger.Error("httpapi: presign get failed", "error", err)
		return ports.PresignedURL{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}
	return presigned, nil
}

// UploadContent is the sandbox-bearer variant (§28.5): GET
// /sessions/{sessionID}/uploads/{uploadID}/content, outside /api and
// outside auth.Middleware -- the download_file tool's own single call.
func UploadContent(sandboxes *postgres.SandboxStore, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts) http.HandlerFunc {
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

		var uploadID pgtype.UUID
		if err := uploadID.Scan(chi.URLParam(r, "uploadID")); err != nil {
			writeError(w, http.StatusNotFound, "upload not found")
			return
		}

		redirectToPresignedGet(ctx, w, r, artifacts, blobStore, objCfg, timeouts, sessionID, uploadID)
	}
}

// UploadContentAPI is the browser variant (§28.5): GET
// /api/sessions/:id/uploads/:uploadId/content -- gated by session
// visibility only (mirrors ListArtifacts/ListEvents' own "session exists +
// authenticated via auth.Middleware, no separate Authorize call"
// precedent exactly): a download is a read, so a read-only viewer may.
func UploadContentAPI(sessions *postgres.SessionStore, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session for content failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var uploadID pgtype.UUID
		if err := uploadID.Scan(chi.URLParam(r, "uploadID")); err != nil {
			writeError(w, http.StatusNotFound, "upload not found")
			return
		}

		redirectToPresignedGet(ctx, w, r, artifacts, blobStore, objCfg, timeouts, sessionID, uploadID)
	}
}

// redirectToPresignedGet is the shared tail both handlers above call:
// resolve the presigned GET URL and 302 to it, or write the appropriate
// error.
func redirectToPresignedGet(ctx context.Context, w http.ResponseWriter, r *http.Request, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, timeouts platform.Timeouts, sessionID, uploadID pgtype.UUID) {
	presigned, uerr := uploadContentCore(ctx, artifacts, blobStore, objCfg, timeouts, sessionID, uploadID)
	if uerr != nil {
		writeError(w, uerr.Status, uerr.Message)
		return
	}
	http.Redirect(w, r, presigned.URL, http.StatusFound)
}
