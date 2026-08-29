package auth

import "net/http"

// fetchSomethingElse proves the baseline is a per-FILE allowance, never a
// per-directory one: a second file in the SAME package as the pinned
// callback.go must still be reported for a new, unaudited call site.
func fetchSomethingElse(url string) (*http.Response, error) {
	return http.Get(url) // want `net/http\.Get is a client-side \(egress\) symbol`
}
