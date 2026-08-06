-- Queries backing ArtifactStore (§4.3, §6.3 GET /api/sessions/:id/
-- artifacts and the client WS hub's own SubscribedPayload.artifacts,
-- §6.2). CreateArtifact is Step 21's ("e2e happy path") own addition --
-- the first real artifact-minting caller anywhere in this codebase
-- (app/sessionactor's own createPRBestEffort records a "pr"-typed artifact
-- once SourceControl.CreatePR succeeds, see pushpr.go) -- previews (Step
-- 48) and uploads (Step 49) remain the only artifact_type values with no
-- Create caller yet.

-- name: CreateArtifact :one
INSERT INTO artifacts (session_id, type, url, metadata)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListArtifactsForSession :many
SELECT * FROM artifacts
WHERE session_id = $1
ORDER BY created_at ASC;

-- Step 58 ("uploads, blob storage & the in-sandbox download_file tool",
-- §28.4): the upload lifecycle's own queries. type is always the literal
-- 'upload' and status always starts 'pending' -- never caller-supplied --
-- so a pending upload row can never be minted with the wrong type or in
-- any status other than 'pending' by construction, unlike CreateArtifact
-- above (which is used for already-resolved pr/preview rows and takes
-- both as parameters).

-- name: CreateUploadArtifact :one
INSERT INTO artifacts (session_id, type, url, status, blob_key, size_bytes, content_type, filename, created_by)
VALUES ($1, 'upload', $2, 'pending', $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetArtifactForSession :one
SELECT * FROM artifacts
WHERE id = $1 AND session_id = $2;

-- ListReadyUploadArtifactsByIDsForSession backs attachmentIds validation
-- at the turn-creation chokepoint (createTurnLocked, §28.5): every
-- requested id must come back in this result, scoped to THIS session and
-- status = 'ready' -- a caller-side count/set comparison against the
-- requested ids catches any unknown, foreign, or not-yet-ready id.
-- name: ListReadyUploadArtifactsByIDsForSession :many
SELECT * FROM artifacts
WHERE session_id = $1 AND type = 'upload' AND status = 'ready' AND id = ANY(sqlc.arg(ids)::uuid[]);

-- SumSessionUploadBytes backs the session-level quota check at both mint
-- (fast-fail courtesy) and confirm (enforcement of record, §28.4):
-- SUM(size_bytes) over the session's OWN pending+ready upload rows,
-- derived from rows that already exist -- never a dedicated counter
-- column (§25.5's own discipline, named directly in §28.4).
-- name: SumSessionUploadBytes :one
SELECT COALESCE(SUM(size_bytes), 0)::bigint AS total_bytes
FROM artifacts
WHERE session_id = $1 AND type = 'upload' AND status IN ('pending', 'ready');

-- MarkUploadArtifactReadyIfPending/MarkUploadArtifactFailedIfPending are
-- the guarded transitions confirm uses (§28.4's own "guarded transition,
-- the §25.6 idiom" -- mirrors ApprovePlanIfAwaitingApproval's identical
-- "WHERE ... status = <the one valid predecessor>" shape): :execrows lets
-- the caller distinguish "this call performed the transition" (1) from
-- "someone else already resolved this row" (0), so a retried confirm of
-- an already-resolved row can re-read and return the recorded outcome
-- instead of re-verifying or double-appending an event.

-- name: MarkUploadArtifactReadyIfPending :execrows
UPDATE artifacts
SET status = 'ready'
WHERE id = $1 AND session_id = $2 AND status = 'pending';

-- name: MarkUploadArtifactFailedIfPending :execrows
UPDATE artifacts
SET status = 'failed', failure_reason = $3
WHERE id = $1 AND session_id = $2 AND status = 'pending';

-- ListPendingUploadArtifactsOlderThan backs the abandonment sweep
-- (§28.4): a pending row older than UploadPendingSweepAfter is a
-- candidate for failed(abandoned). limitCount bounds one sweep pass
-- (mirrors ListOrphanedStarting's own batching precedent in
-- internal/app/automation/sweep.go).
-- name: ListPendingUploadArtifactsOlderThan :many
SELECT * FROM artifacts
WHERE type = 'upload' AND status = 'pending' AND created_at < $1
ORDER BY created_at ASC
LIMIT $2;
