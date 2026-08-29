package boot

import "net/http"

// fetch stands for the real sandbox agent's own legitimate client use --
// must NOT be reported.
func fetch(url string) (*http.Response, error) {
	return http.Get(url)
}
