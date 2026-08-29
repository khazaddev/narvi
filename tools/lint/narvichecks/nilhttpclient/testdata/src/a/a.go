package a

import "net/http"

type Client struct{ httpClient *http.Client }

// New mirrors the outbound constructors' real shape: an *http.Client
// first parameter with no usable default behind it.
func New(httpClient *http.Client, baseURL string) *Client { return &Client{httpClient: httpClient} }

// NewWithToken proves the check is positional, not name-based: the
// *http.Client is not the first parameter here.
func NewWithToken(token string, httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

// takesInterface proves the check is about *http.Client specifically --
// a nil for some other pointer or interface parameter is none of its
// business.
func takesInterface(rt http.RoundTripper) {}

func wireBad() {
	_ = New(nil, "https://example.invalid") // want "passing nil as the \\*http.Client"
}

func wireBadSecondPosition() {
	_ = NewWithToken("t", nil) // want "passing nil as the \\*http.Client"
}

func wireGood() {
	_ = New(http.DefaultClient, "https://example.invalid")
	_ = New(&http.Client{}, "https://example.invalid")
}

func unrelatedNil() {
	takesInterface(nil)
}
