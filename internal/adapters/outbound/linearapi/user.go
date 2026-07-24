package linearapi

import "context"

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

// GetUserEmail returns the email of the Linear user identified by userID,
// authenticated with accessToken (the SAME per-workspace installation
// token every other call in this package already uses -- Linear's API has
// no separate "read any user in this workspace" credential). A GraphQL-
// level error (Linear's own "not found"/permission-denied field errors,
// surfaced by doGraphQL as *graphQLResponseError) or an HTTP-level failure
// (*httpStatusError, or a plain transport error) is returned unwrapped --
// this package does no retryable/permanent classification of its own
// (mirrors doGraphQL's own established "no transient/permanent
// classification" precedent, see graphQLResponseError's own doc comment);
// internal/app/identitylink.Resolve is the caller that wraps this in
// platform.Retry and decides what, if anything, counts as permanent.
func (c *Client) GetUserEmail(ctx context.Context, accessToken, userID string) (string, error) {
	variables := map[string]any{"id": userID}
	var data getUserEmailData
	if err := c.doGraphQL(ctx, accessToken, getUserEmailQuery, variables, &data); err != nil {
		return "", err
	}
	return data.User.Email, nil
}
