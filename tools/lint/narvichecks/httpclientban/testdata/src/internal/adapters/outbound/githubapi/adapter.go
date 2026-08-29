package githubapi

import (
	"context"
	"net/http"
)

// Adapter stands for the real outbound adapter's own legitimate client
// construction and use -- must NOT be reported, anywhere in this file.
type Adapter struct {
	httpClient *http.Client
}

func New(httpClient *http.Client) *Adapter {
	if httpClient == nil {
		httpClient = &http.Client{Transport: http.DefaultTransport}
	}
	return &Adapter{httpClient: httpClient}
}

func (a *Adapter) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return a.httpClient.Do(req)
}
