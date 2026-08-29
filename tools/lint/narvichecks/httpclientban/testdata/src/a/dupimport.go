package a

import (
	// A blank import of the same path, justified so revive's own
	// blank-imports rule is satisfied -- which is what made this a
	// realistic evasion rather than a contrived one.
	_ "net/http"

	"net/http"
	"net/url"
)

// Go allows one path to be imported more than once, and gofmt sorts the
// blank spec FIRST ("_" before any letter). An analyzer that gave up on
// the first unusable spec therefore skipped this whole file -- in the
// formatter-stable, CI-approved arrangement. Both symbols below must be
// reported.
func dupImportBypass(u string) {
	_, _ = http.Get(u)     // want "net/http.Get is a client-side"
	_ = http.DefaultClient // want "net/http.DefaultClient is a client-side"
}

// postFormIsBanned pins the fourth package-level request function.
// net/http has exactly four -- Get, Head, Post, PostForm -- and PostForm
// is the idiom the Slack Web API itself takes, so omitting it left the
// most likely next ingress reply un-caught.
func postFormIsBanned(u string) {
	_, _ = http.PostForm(u, url.Values{"text": {"hi"}}) // want "net/http.PostForm is a client-side"
}
