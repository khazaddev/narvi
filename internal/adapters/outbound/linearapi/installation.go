package linearapi

import "context"

// viewerAndOrganizationQuery fetches exactly the two ids the OAuth
// install-callback needs to key/label a stored linear_installations row --
// verified against Linear's real schema/docs during §8.10's own
// investigation: "Your app will have a unique ID for each workspace it is
// installed within, you can find this ID with the following query...
// query Me { viewer { id } }" (the organization id is fetched in the same
// round trip via the top-level `organization` query field, rather than a
// second request).
const viewerAndOrganizationQuery = `
query NarviLinearInstallIdentity {
  viewer {
    id
  }
  organization {
    id
  }
}
`

type viewerAndOrganizationData struct {
	Viewer struct {
		ID string `json:"id"`
	} `json:"viewer"`
	Organization struct {
		ID string `json:"id"`
	} `json:"organization"`
}

// ViewerAndOrganization returns the app-user id and organization
// (workspace) id for the workspace accessToken was just issued for --
// called once, right after a successful OAuth token exchange, so the
// resulting linear_installations row can be keyed by organization id
// (matching the SAME id Linear's own AgentSessionEventWebhookPayload.
// organizationId field carries on every later webhook).
func (c *Client) ViewerAndOrganization(ctx context.Context, accessToken string) (appUserID, organizationID string, err error) {
	var data viewerAndOrganizationData
	if err := c.doGraphQL(ctx, accessToken, viewerAndOrganizationQuery, nil, &data); err != nil {
		return "", "", err
	}
	return data.Viewer.ID, data.Organization.ID, nil
}
