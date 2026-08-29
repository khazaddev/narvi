package a

import (
	"context"
	"net/http"
)

// fetch is the mistake this analyzer exists to catch: an ordinary
// in-process package reaching out over the network directly, outside
// every gated outbound client.
func fetch(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // want `net/http\.NewRequestWithContext is a client-side \(egress\) symbol`
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req) // want `net/http\.DefaultClient is a client-side \(egress\) symbol`
}

// serve proves the check is narrow: server-side net/http (handling a
// request, answering with a status constant) is untouched everywhere.
func serve(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

var _ http.HandlerFunc = serve
