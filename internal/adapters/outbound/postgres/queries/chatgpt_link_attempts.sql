-- Queries backing ChatGPTLinkAttemptStore -- the ChatGPT-account-OAuth
-- link flow's own short-lived nonce table (Step 59, §29.3,
-- migrations/000062_chatgpt_oauth_credentials.up.sql). Mirrors identity_
-- link_prompts.sql's own query shapes (queries/identity_link_prompts.sql)
-- adapted for a direct per-user link rather than a provider+external-id
-- match -- see that file's own doc comment for the shared "no UNIQUE
-- constraint, resend/reuse policy lives in app code" precedent this table
-- also follows.

-- name: CreateChatGPTLinkAttempt :one
INSERT INTO chatgpt_link_attempts (user_id, device_auth_id, user_code, interval_seconds, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetLatestChatGPTLinkAttemptForUser :one
-- internal/app/chatgptlink's own re-entry check (mirrors identity_link_
-- prompts.sql's GetLatestLinkPromptForProviderAndExternalID exactly): the
-- most recently created attempt for this user, if any, so StartLink can
-- decide whether a still-live one already exists before minting a brand
-- new device code on every single "Connect ChatGPT account" click -- and
-- so PollLink can find the current attempt to poll against.
SELECT * FROM chatgpt_link_attempts
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateChatGPTLinkAttemptLastPolledAt :one
-- Throttles PollLink's own upstream deviceauth/token call to at most one
-- attempt per server-provided interval (§29.3 point 2) -- the caller reads
-- last_polled_at first, decides whether enough time has passed, and only
-- then calls this to record a fresh attempt timestamp before actually
-- polling upstream.
UPDATE chatgpt_link_attempts
SET last_polled_at = $2
WHERE id = $1
RETURNING *;

-- name: DeleteChatGPTLinkAttempt :exec
DELETE FROM chatgpt_link_attempts
WHERE id = $1;

-- name: DeleteChatGPTLinkAttemptsForUser :exec
-- Cleanup once a user's link genuinely succeeds (mirrors identity_link_
-- prompts.sql's own DeleteLinkPromptsForProviderAndExternalID) -- any
-- other still-pending attempt for that same user is deleted too, so a
-- stale device code minted before this success can never be polled again
-- afterward.
DELETE FROM chatgpt_link_attempts
WHERE user_id = $1;
