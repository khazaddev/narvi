// This file (uploadconfirm.go) implements the confirm half of §8.6's
// upload lifecycle (§28.4/§28.6): POST .../uploads/{uploadId}/complete, in
// both auth variants. Confirm Stats the object and verifies existence +
// actual size == declared, re-checks both size/quota limits NOW (the
// enforcement of record), and transitions pending -> ready|failed(reason)
// via a guarded UPDATE, in the same transaction as an appended `artifact`
// event, broadcast only after commit -- a failing transition also
// outboxes a blob_delete entry in that SAME transaction. Idempotent: a
// retried confirm of an already-resolved row returns the recorded outcome
// without ever calling Stat again (§28.4).
//
// Artifact rows are not actor-owned state (§2's single-writer rule covers
// session/sandbox/turn rows only) -- pushpr.go's own direct
// github_pr_sessions writes are the accepted precedent for this class of
// write (§24.1), so this handler writes directly in its own transaction
// rather than routing through app/sessionactor.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
	"github.com/narvidev/narvi/internal/adapters/outbound/objstore"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
	domainupload "github.com/narvidev/narvi/internal/domain/upload"
	"github.com/narvidev/narvi/internal/platform"
)

// confirmOutcome is confirmUploadCore's own return shape -- converted by
// each of this file's two handlers into ITS OWN wire response type.
type confirmOutcome struct {
	Status        sqlcgen.ArtifactStatus
	FailureReason *sqlcgen.ArtifactFailureReason
}

// confirmUploadCore is the shared logic both auth variants run after
// their own, DIFFERENT auth check passes.
func confirmUploadCore(
	ctx context.Context,
	pool *pgxpool.Pool,
	artifacts *postgres.ArtifactStore,
	events *postgres.EventStore,
	outbox *postgres.OutboxStore,
	sandboxes *postgres.SandboxStore,
	broadcaster ports.EventBroadcaster,
	blobStore ports.BlobStore,
	objCfg *platform.ObjectStorageConfig,
	sessionID, uploadID pgtype.UUID,
) (confirmOutcome, *uploadError) {
	logger := platform.Logger(ctx)

	row, err := artifacts.GetForSession(ctx, uploadID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return confirmOutcome{}, &uploadError{Status: http.StatusNotFound, Message: "upload not found"}
		}
		logger.Error("httpapi: get artifact for confirm failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}
	if row.Type != sqlcgen.ArtifactTypeUpload {
		return confirmOutcome{}, &uploadError{Status: http.StatusNotFound, Message: "upload not found"}
	}

	// Idempotent retry (§28.4): a row already resolved (by an earlier
	// confirm call, or by the abandonment sweep) returns its recorded
	// outcome directly -- crucially, WITHOUT calling Stat again: a
	// 'failed' row may already have had its own blob deleted by the
	// blob_delete outbox entry THIS same resolution enqueued, so a second
	// Stat here could misleadingly observe ErrBlobNotFound for a reason
	// that has nothing to do with verification.
	if row.Status != sqlcgen.ArtifactStatusPending {
		return confirmOutcome{Status: row.Status, FailureReason: row.FailureReason}, nil
	}

	if objCfg == nil || blobStore == nil {
		logger.Error("httpapi: confirm called for a pending upload but object storage is not configured")
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	declaredSize := int64OrZero(row.SizeBytes)
	blobKey := ports.BlobKey(stringOrEmpty(row.BlobKey))

	reason, retryable := evaluateConfirmOutcome(ctx, artifacts, blobStore, objCfg, sessionID, blobKey, declaredSize)
	if retryable != nil {
		return confirmOutcome{}, retryable
	}

	// Current sandbox gen for the synthesized artifact event (§6.1's own
	// required field) -- purely informational for a CP-synthesized event,
	// never gen-fenced the way a sandbox-originated one is (§28.6: "CP-
	// synthesized only").
	var gen int32
	if sandboxRow, sbErr := sandboxes.Get(ctx, sessionID); sbErr == nil {
		gen = sandboxRow.Gen
	} else if !errors.Is(sbErr, pgx.ErrNoRows) {
		logger.Warn("httpapi: get sandbox gen for artifact event failed; defaulting to 0", "error", sbErr)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: begin confirm tx failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var rowsAffected int64
	var finalStatus sqlcgen.ArtifactStatus
	var finalReason *sqlcgen.ArtifactFailureReason
	if reason == "" {
		rowsAffected, err = artifacts.WithTx(tx).MarkUploadReadyIfPending(ctx, uploadID, sessionID)
		finalStatus = sqlcgen.ArtifactStatusReady
	} else {
		sqlcReason := sqlcgen.ArtifactFailureReason(reason)
		rowsAffected, err = artifacts.WithTx(tx).MarkUploadFailedIfPending(ctx, uploadID, sessionID, sqlcReason)
		finalStatus = sqlcgen.ArtifactStatusFailed
		finalReason = &sqlcReason
	}
	if err != nil {
		logger.Error("httpapi: guarded upload status transition failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	if rowsAffected == 0 {
		// Lost the race to a concurrent confirm/sweep between the initial
		// GetForSession above and this UPDATE -- re-read and return
		// whatever it recorded, exactly like the up-front already-resolved
		// check above.
		latest, getErr := artifacts.GetForSession(ctx, uploadID, sessionID)
		if getErr != nil {
			logger.Error("httpapi: re-read artifact after lost transition race failed", "error", getErr)
			return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
		}
		return confirmOutcome{Status: latest.Status, FailureReason: latest.FailureReason}, nil
	}

	wireStatus := sandboxws.ArtifactStatus(finalStatus)
	var wireFailureReason *sandboxws.ArtifactFailureReason
	if finalReason != nil {
		wireFailureReason = &sandboxws.ArtifactFailureReason{Value: string(*finalReason)}
	}
	eventPayload, err := json.Marshal(sandboxws.Artifact{
		Type:          "artifact",
		MessageId:     uuid.NewString(),
		SessionId:     sessionID.String(),
		Gen:           int(gen),
		ArtifactType:  sandboxws.ArtifactArtifactTypeUpload,
		Url:           row.Url,
		Metadata:      sandboxws.ArtifactMetadata{},
		Status:        &wireStatus,
		FailureReason: wireFailureReason,
	})
	if err != nil {
		logger.Error("httpapi: marshal artifact event failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	createdEvent, err := events.WithTx(tx).Create(ctx, sqlcgen.CreateEventParams{
		SessionID: sessionID,
		Type:      "artifact",
		MessageID: uuid.NewString(),
		Payload:   eventPayload,
	})
	if err != nil {
		logger.Error("httpapi: append artifact event failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	if reason != "" {
		blobDeletePayload, marshalErr := json.Marshal(objstore.BlobDeletePayload{Key: string(blobKey)})
		if marshalErr != nil {
			logger.Error("httpapi: marshal blob_delete outbox payload failed", "error", marshalErr)
			return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
		}
		if _, err := outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID: sessionID,
			Kind:      string(ports.NotificationKindBlobDelete),
			Payload:   blobDeletePayload,
		}); err != nil {
			logger.Error("httpapi: enqueue blob_delete outbox entry failed", "error", err)
			return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: commit confirm tx failed", "error", err)
		return confirmOutcome{}, &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}

	// Broadcast only after commit (ports.EventBroadcaster's own contract)
	// -- and only when this call actually inserted the row (Create's own
	// dedupe-on-message-id semantics; always true here, this messageId is
	// freshly generated above, but checked anyway to mirror
	// sessionactor.Actor.appendRawEvent's own identical guard).
	if broadcaster != nil && createdEvent.Inserted {
		broadcaster.Broadcast(sessionID.String(), eventPayload)
	}

	return confirmOutcome{Status: finalStatus, FailureReason: finalReason}, nil
}

// evaluateConfirmOutcome runs Stat and, if the object genuinely exists and
// matches, the re-checked size/quota evaluation -- returning ("", nil) on
// success, (a FailureReason, nil) on a definitive failure the caller
// should persist, or ("", a retryable *uploadError) when Stat itself
// failed transiently and the row should be left 'pending' for a later
// retry rather than permanently failed over a storage blip.
func evaluateConfirmOutcome(ctx context.Context, artifacts *postgres.ArtifactStore, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig, sessionID pgtype.UUID, blobKey ports.BlobKey, declaredSize int64) (domainupload.FailureReason, *uploadError) {
	logger := platform.Logger(ctx)

	info, statErr := blobStore.Stat(ctx, blobKey)
	switch {
	case statErr == nil && info.SizeBytes != declaredSize:
		return domainupload.FailureReasonVerificationFailed, nil
	case statErr != nil && errors.Is(statErr, ports.ErrBlobNotFound):
		// The ONLY Stat outcome that genuinely PROVES the object is
		// absent -- a real verification failure, safe to persist as
		// terminal (MarkUploadFailedIfPending below) and to outbox a
		// blob_delete for.
		return domainupload.FailureReasonVerificationFailed, nil
	case statErr != nil:
		// Every OTHER Stat error -- transient (network blip, 5xx, 429)
		// OR permanent-but-non-not-found (401/403/409/413/422: a rotated
		// object-store secret, an IAM/bucket-policy change, SigV4 clock
		// skew, ...) -- says NOTHING about whether the object's bytes
		// actually arrived: a 401/403 is just as consistent with "the
		// upload succeeded and this row should end up ready" as with any
		// other outcome. Fail-safe direction, always: never destroy a
		// possibly-intact row/object on an error that doesn't PROVE
		// absence. Both classes are therefore treated identically here --
		// leave the row 'pending' and return a retryable 500, exactly
		// like the transient case always has:
		//   - a genuinely transient error clears on its own retry;
		//   - a permanent credential/policy error clears once an
		//     operator fixes the underlying credential/policy -- and the
		//     row is STILL 'pending' (not yet the terminal 'failed'
		//     state confirmUploadCore's own idempotency guard makes
		//     unrecoverable) at that point, so the very next confirm
		//     retry Stats again and can still resolve to 'ready'.
		// Before this fix, the permanent branch alone mapped straight to
		// verification_failed -- marking a successfully-uploaded row
		// failed AND outboxing a blob_delete for a possibly-intact
		// object, destroying good data the moment credentials were
		// restored. If a Stat error genuinely never clears, the 24h
		// abandonment sweep (uploadsweep) is the eventual backstop that
		// reaps the still-pending row.
		logger.Warn("httpapi: stat during confirm failed; leaving upload pending for a later retry", "error", statErr, "transient", ports.IsBlobStoreTransient(statErr))
		return "", &uploadError{Status: http.StatusInternalServerError, Message: "verification temporarily unavailable, please retry"}
	}

	// Stat succeeded and size matches: re-check size/quota NOW, the
	// enforcement of record (§28.4) -- excluding THIS row's own declared
	// size from the session total before re-adding its ACTUAL size, since
	// this row (still 'pending' at read time) is already counted once by
	// SumSessionUploadBytes.
	totalIncludingThis, err := artifacts.SumSessionUploadBytes(ctx, sessionID)
	if err != nil {
		logger.Error("httpapi: sum session upload bytes at confirm failed", "error", err)
		return "", &uploadError{Status: http.StatusInternalServerError, Message: "internal error"}
	}
	totalExcludingThis := totalIncludingThis - declaredSize
	if evalReason, ok := domainupload.EvaluateUploadSize(info.SizeBytes, objCfg.MaxUploadBytes, totalExcludingThis, objCfg.MaxSessionUploadBytes); !ok {
		return evalReason, nil
	}
	return "", nil
}

// ConfirmUpload is the sandbox-bearer variant (§28.5): POST
// /sessions/{sessionID}/uploads/{uploadID}/complete, outside /api and
// outside auth.Middleware.
func ConfirmUpload(sandboxes *postgres.SandboxStore, pool *pgxpool.Pool, artifacts *postgres.ArtifactStore, events *postgres.EventStore, outbox *postgres.OutboxStore, broadcaster ports.EventBroadcaster, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig) http.HandlerFunc {
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

		outcome, uerr := confirmUploadCore(ctx, pool, artifacts, events, outbox, sandboxes, broadcaster, blobStore, objCfg, sessionID, uploadID)
		if uerr != nil {
			writeError(w, uerr.Status, uerr.Message)
			return
		}

		writeJSON(w, http.StatusOK, uploadConfirmResponse{
			Status:        string(outcome.Status),
			FailureReason: failureReasonPtrToString(outcome.FailureReason),
		})
	}
}

// ConfirmUploadAPI is the browser variant (§28.5): POST
// /api/sessions/:id/uploads/:uploadId/complete, gated by the SAME
// authz.ActionUploadToSession check as MintUploadAPI (uploadmint.go).
func ConfirmUploadAPI(sessions *postgres.SessionStore, participants *postgres.ParticipantStore, pool *pgxpool.Pool, artifacts *postgres.ArtifactStore, events *postgres.EventStore, outbox *postgres.OutboxStore, sandboxes *postgres.SandboxStore, broadcaster ports.EventBroadcaster, blobStore ports.BlobStore, objCfg *platform.ObjectStorageConfig) http.HandlerFunc {
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

		var uploadID pgtype.UUID
		if err := uploadID.Scan(chi.URLParam(r, "uploadID")); err != nil {
			writeError(w, http.StatusNotFound, "upload not found")
			return
		}

		outcome, uerr := confirmUploadCore(ctx, pool, artifacts, events, outbox, sandboxes, broadcaster, blobStore, objCfg, sessionID, uploadID)
		if uerr != nil {
			logger.Error("httpapi: confirm upload failed", "status", uerr.Status, "message", uerr.Message)
			writeError(w, uerr.Status, uerr.Message)
			return
		}

		writeJSON(w, http.StatusOK, restdtos.ConfirmUploadResponse{
			Status:        restdtos.ConfirmUploadResponseStatus(outcome.Status),
			FailureReason: restFailureReason(outcome.FailureReason),
		})
	}
}

// uploadConfirmResponse is the sandbox-bearer confirm variant's own
// plain, un-schema'd wire type -- see uploadMintRequest's own doc comment
// (uploadmint.go) for why this package never shares a contracts/rest type
// with a sandbox-bearer endpoint.
type uploadConfirmResponse struct {
	Status        string  `json:"status"`
	FailureReason *string `json:"failureReason"`
}

func failureReasonPtrToString(r *sqlcgen.ArtifactFailureReason) *string {
	if r == nil {
		return nil
	}
	s := string(*r)
	return &s
}

func restFailureReason(r *sqlcgen.ArtifactFailureReason) *restdtos.ConfirmUploadResponseFailureReason {
	if r == nil {
		return nil
	}
	return &restdtos.ConfirmUploadResponseFailureReason{Value: string(*r)}
}
