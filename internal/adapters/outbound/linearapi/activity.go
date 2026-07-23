package linearapi

import "context"

// createAgentActivityMutation is Linear's own real agentActivityCreate
// mutation (verified against Linear's real GraphQL schema -- schema.
// graphql's own Mutation.agentActivityCreate(input: AgentActivityCreateInput!):
// AgentActivityPayload! -- and its own developer docs' worked example
// during Step 34's investigation). content is passed as a raw JSONObject,
// matching AgentActivityCreateInput.content's own schema type exactly
// ("This object is not strictly typed. See ... for typing details.").
const createAgentActivityMutation = `
mutation NarviCreateAgentActivity($input: AgentActivityCreateInput!) {
  agentActivityCreate(input: $input) {
    success
  }
}
`

type createAgentActivityData struct {
	AgentActivityCreate struct {
		Success bool `json:"success"`
	} `json:"agentActivityCreate"`
}

// thoughtContent is the "thought" activity content shape (Linear's real
// docs: `{"content": {"type": "thought", "body": "..."}}`) -- one of the
// five allowed activity content types; this Step's own scope only ever
// emits this one (a minimal, immediate acknowledgment, per this package's
// own doc.go), never action/response/error/elicitation.
type thoughtContent struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

// CreateThoughtActivity posts a single `thought` Agent Activity to
// agentSessionID, authenticated as the workspace's own installed app
// (accessToken, from a stored linear_installations row). This is the
// minimal, immediate acknowledgment Linear's own docs require within 10
// seconds of a `created` AgentSessionEvent to avoid the session being
// marked unresponsive -- see this package's own doc.go for why this is a
// direct, synchronous call rather than routed through an outbox.
func (c *Client) CreateThoughtActivity(ctx context.Context, accessToken, agentSessionID, body string) error {
	variables := map[string]any{
		"input": map[string]any{
			"agentSessionId": agentSessionID,
			"content": thoughtContent{
				Type: "thought",
				Body: body,
			},
		},
	}
	var data createAgentActivityData
	return c.doGraphQL(ctx, accessToken, createAgentActivityMutation, variables, &data)
}
