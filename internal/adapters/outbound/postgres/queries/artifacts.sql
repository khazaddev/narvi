-- Queries backing ArtifactStore (§4.3, §6.3 GET /api/sessions/:id/
-- artifacts and the client WS hub's own SubscribedPayload.artifacts,
-- §6.2). ListForSession only -- nothing in the codebase mints an artifact
-- row yet (real artifact CREATION is a later Step: PR creation is Step
-- 21+, previews Step 48, uploads Step 49), so no Create query exists here
-- either.

-- name: ListArtifactsForSession :many
SELECT * FROM artifacts
WHERE session_id = $1
ORDER BY created_at ASC;
