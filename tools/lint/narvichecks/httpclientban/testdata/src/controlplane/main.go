package controlplane

import "net/http"

type adapter struct{ httpClient *http.Client }

func newAdapter(httpClient *http.Client) *adapter { return &adapter{httpClient: httpClient} }

// wireAdapter stands for the real composition root's own legitimate
// construction: passing http.DefaultClient into an outbound adapter's
// constructor -- must NOT be reported.
func wireAdapter() *adapter {
	return newAdapter(http.DefaultClient)
}

// issueRequestDirectly is the mistake even the composition root may not
// make: constructing is this tree's job, issuing a live request from here
// is not -- must be reported.
func issueRequestDirectly(url string) (*http.Response, error) {
	return http.Get(url) // want `net/http\.Get is a client-side \(egress\) symbol`
}
