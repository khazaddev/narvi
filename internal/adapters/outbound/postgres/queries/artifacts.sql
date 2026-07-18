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
