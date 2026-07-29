package linearapi

import (
	"context"
	"errors"
)

// getUserEmailQuery fetches a single Linear User's own email by id --
// Linear's real schema exposes a top-level `user(id: String!): User!`
// Query field, and User.email is a required (non-null) String on Linear's
// own real schema (both verified against Linear's current GraphQL schema/
// developer docs during this Step's investigation). Used by Step 39's
// ("identities + full RBAC", §13.2) own auto-link algorithm to resolve
// the human actor behind an AgentSessionWebhookPayload.creatorId --
// distinct from ViewerAndOrganization (installation.go), which resolves
// the INSTALLING app-user/organization, not an arbitrary session's own
// human creator.
const getUserEmailQuery = `
query NarviGetUserEmail($id: String!) {
  user(id: $id) {
    email
  }
}
`

type getUserEmailData struct {
	User struct {
		Email string `json:"email"`
	} `json:"user"`
}

// ErrLinearUserNotFound is Linear's own real GraphQL error for a user id
// that doesn't exist (or is no longer visible to this app -- e.g. removed
// from the workspace) -- Linear returns this as a field-level error
// ("Entity not found", verified against this package's own existing
// TestGetUserEmail_GraphQLError fixture, itself confirmed against Linear's
// real API behavior) rather than an HTTP-level failure. Audit fix (the
// Linear-side analog of slackapi.ErrSlackUserNotFound): this is the ONE
// GetUserEmail error internal/adapters/inbound/linear/identity.go's own
// fetch closure treats as PERMANENT (wrapped via platform.Permanent, then
// checked by internal/app/identitylink.FetchEmailWithRetry before
// platform.Retry's own unwrapping) -- routine workspace member churn (a
// deactivated/removed Linear user), not evidence the profile-email fetch
// API itself is broken. Every OTHER GraphQL/HTTP/transport error from this
// call is left unclassified, since §13.2 requires a genuine fetch FAILURE
// to be retried, never silently treated as "this identity has no email".
var ErrLinearUserNotFound = errors.New("linearapi: entity not found")

// GetUserEmail returns the email of the Linear user identified by userID,
// authenticated with accessToken (the SAME per-workspace installation
// token every other call in this package already uses -- Linear's API has
// no separate "read any user in this workspace" credential). Linear's own
// "Entity not found" GraphQL error, for a user id that no longer resolves,
// surfaces as ErrLinearUserNotFound (see that sentinel's own doc comment);
// any OTHER GraphQL-level error (permission-denied, or anything else
// doGraphQL surfaces as *graphQLResponseError), or an HTTP-level failure
// (*httpStatusError, or a plain transport error), is returned unwrapped --
// this package does no further retryable/permanent classification of its
// own (mirrors doGraphQL's own established "no transient/permanent
// classification" precedent, see graphQLResponseError's own doc comment).
// internal/adapters/inbound/linear/identity.go's own fetch closure (NOT
// internal/app/identitylink.Resolve, which never sees a raw error at all)
// is what checks errors.Is(err, ErrLinearUserNotFound) and wraps it via
// platform.Permanent before internal/app/identitylink.FetchEmailWithRetry
// hands it to platform.Retry -- mirroring internal/adapters/inbound/
// slack/identity.go's own identical pattern for ErrSlackUserNotFound.
func (c *Client) GetUserEmail(ctx context.Context, accessToken, userID string) (string, error) {
	variables := map[string]any{"id": userID}
	var data getUserEmailData
	if err := c.doGraphQL(ctx, accessToken, getUserEmailQuery, variables, &data); err != nil {
		var gqlErr *graphQLResponseError
		if errors.As(err, &gqlErr) {
			for _, msg := range gqlErr.Messages {
				if msg == "Entity not found" {
					return "", ErrLinearUserNotFound
				}
			}
		}
		return "", err
	}
	return data.User.Email, nil
}
