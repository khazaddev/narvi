package auth

import (
	"context"
	"net/http"
)

// fetchGitHubUser stands for the real callback.go's own pinned, audited
// baseline entry -- an OAuth sign-in identity read. Must NOT be reported:
// this exact file is this package's own baseline.
func fetchGitHubUser(ctx context.Context, client *http.Client, apiBaseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/user", nil)
	if err != nil {
		return err
	}
	_, err = client.Do(req)
	return err
}
