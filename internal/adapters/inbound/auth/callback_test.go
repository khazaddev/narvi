package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestFetchGitHubUser proves fetchGitHubUser parses id/login from a canned
// GET /user response shaped exactly like GitHub's own documented response.
func TestFetchGitHubUser(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 12345, "login": "octocat", "name": "The Octocat", "email": null}`))
	}))
	defer server.Close()

	got, err := fetchGitHubUser(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchGitHubUser() error = %v", err)
	}
	if got.ID != 12345 {
		t.Errorf("ID = %d, want 12345", got.ID)
	}
	if got.Login != "octocat" {
		t.Errorf("Login = %q, want %q", got.Login, "octocat")
	}
}

// TestFetchGitHubUser_NonOKStatus proves a non-200 /user response surfaces
// as an error, never a zero-value githubUser mistaken for a real one.
func TestFetchGitHubUser_NonOKStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := fetchGitHubUser(t.Context(), server.Client(), server.URL); err == nil {
		t.Fatal("fetchGitHubUser() error = nil, want error for a 401 response")
	}
}

// TestFetchVerifiedPrimaryEmail is table-driven over several /user/emails
// response shapes: an entry with primary&&verified is found regardless of
// its position in the array; an entry that is primary but NOT verified is
// never trusted (§13's own explicit requirement -- never fall back to an
// unverified email); no entry at all is reported as "not found", never an
// error.
func TestFetchVerifiedPrimaryEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		wantEmail string
		wantFound bool
	}{
		{
			name:      "single verified primary",
			body:      `[{"email":"a@example.com","primary":true,"verified":true}]`,
			wantEmail: "a@example.com",
			wantFound: true,
		},
		{
			name:      "verified primary found among several entries",
			body:      `[{"email":"secondary@example.com","primary":false,"verified":true},{"email":"primary@example.com","primary":true,"verified":true}]`,
			wantEmail: "primary@example.com",
			wantFound: true,
		},
		{
			name:      "primary but unverified is never trusted",
			body:      `[{"email":"unverified@example.com","primary":true,"verified":false}]`,
			wantFound: false,
		},
		{
			name:      "verified but not primary is never trusted",
			body:      `[{"email":"notprimary@example.com","primary":false,"verified":true}]`,
			wantFound: false,
		},
		{
			name:      "empty array",
			body:      `[]`,
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user/emails" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			email, found, err := fetchVerifiedPrimaryEmail(t.Context(), server.Client(), server.URL)
			if err != nil {
				t.Fatalf("fetchVerifiedPrimaryEmail() error = %v", err)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if found && email != tc.wantEmail {
				t.Errorf("email = %q, want %q", email, tc.wantEmail)
			}
		})
	}
}

// TestCheckOrgMembership_StatusCodeSemantics covers all 3 real GitHub
// status codes for GET /orgs/{org}/members/{username} (verified live
// against docs.github.com, see checkOrgMembership's own doc comment): only
// 204 is treated as "member"; 404 and 302 are both treated as "not a
// member", the whole point of this function's own fail-closed design.
func TestCheckOrgMembership_StatusCodeSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		wantMember bool
	}{
		{name: "204 is a member", statusCode: http.StatusNoContent, wantMember: true},
		{name: "404 is not a member (requester IS an org member)", statusCode: http.StatusNotFound, wantMember: false},
		{name: "302 is not a member (requester is NOT an org member at all)", statusCode: http.StatusFound, wantMember: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/orgs/my-org/members/octocat"
				if r.URL.Path != wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
				}
				if tc.statusCode == http.StatusFound {
					w.Header().Set("Location", "/some-other-page")
				}
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			client := server.Client()
			// Matches callback.go's own production wiring: never follow
			// redirects automatically, so a real 302 is observed as-is.
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			}

			got := checkOrgMembership(t.Context(), client, server.URL, "my-org", "octocat")
			if got != tc.wantMember {
				t.Errorf("checkOrgMembership() = %v, want %v", got, tc.wantMember)
			}
		})
	}
}

// TestCheckAnyOrgMembership_AnySinglePassIsEnough proves ANY one 204 among
// several configured orgs is sufficient, regardless of order.
func TestCheckAnyOrgMembership_AnySinglePassIsEnough(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/org-a/members/octocat":
			w.WriteHeader(http.StatusNotFound)
		case "/orgs/org-b/members/octocat":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	got := checkAnyOrgMembership(t.Context(), server.Client(), server.URL, "octocat", []string{"org-a", "org-b"})
	if !got {
		t.Error("checkAnyOrgMembership() = false, want true (org-b returns 204)")
	}
}

// TestCheckAnyOrgMembership_NoneMatchFails proves that when no configured
// org returns 204, the overall check fails.
func TestCheckAnyOrgMembership_NoneMatchFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	got := checkAnyOrgMembership(t.Context(), server.Client(), server.URL, "octocat", []string{"org-a", "org-b"})
	if got {
		t.Error("checkAnyOrgMembership() = true, want false (neither org returns 204)")
	}
}

// TestCheckOrgMembership_EscapesPathSegments proves a username (githubUser.
// Login, attested by GitHub but never independently validated by this
// codebase's own character-set rules) containing a "/" is sent as a
// single, correctly-escaped path segment on the wire -- asserted on the
// ACTUAL request the fake test server receives, not merely on the
// client-side URL string.
//
// Go's own net/http server-side r.URL.Path is NOT the right field to
// assert on here: net/url decodes "%2F" back into a literal "/" when
// populating Path (verified directly against this exact stdlib version
// during this test's own design), so a request that WAS correctly
// escaped and one that was naively concatenated unescaped produce an
// IDENTICAL decoded r.URL.Path either way -- asserting equality there
// would pass even with the vulnerable code this fix replaces, proving
// nothing. r.URL.EscapedPath() (backed by RawPath) and r.RequestURI, by
// contrast, preserve the literal wire bytes as sent -- "%2F" stays
// "%2F", never silently collapsed back into an unescaped "/" -- which is
// exactly the representation a real router (GitHub's own, or any
// pattern-matching router that treats "%2F" as distinct from a literal
// path separator) uses to decide segment boundaries. This test asserts
// on that wire-level representation, and additionally proves it
// genuinely differs from what a naive, unescaped concatenation of the
// exact same inputs would have produced.
func TestCheckOrgMembership_EscapesPathSegments(t *testing.T) {
	t.Parallel()

	const org = "my-org"
	const maliciousUsername = "evil/other-user"

	var gotRequestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	got := checkOrgMembership(t.Context(), client, server.URL, org, maliciousUsername)
	if !got {
		t.Fatal("checkOrgMembership() = false, want true (the fake server always replies 204)")
	}

	wantEscaped := "/orgs/" + org + "/members/" + url.PathEscape(maliciousUsername)
	if gotRequestURI != wantEscaped {
		t.Errorf("server observed request-target = %q, want %q (the embedded \"/\" in username must reach the wire escaped "+
			"as a single path segment, not as a raw path separator)", gotRequestURI, wantEscaped)
	}

	// Contrast: a naive, unescaped concatenation (the vulnerable code this
	// fix replaces) would have put a literal, unescaped "/" on the wire
	// here instead, splitting the intended single segment into two for
	// any router that respects RFC 3986 escaping semantics.
	naiveRequestTarget := "/orgs/" + org + "/members/" + maliciousUsername
	if gotRequestURI == naiveRequestTarget {
		t.Errorf("request-target %q is identical to the naive, unescaped concatenation %q -- escaping had no observable effect on the wire",
			gotRequestURI, naiveRequestTarget)
	}
	if !strings.Contains(gotRequestURI, "%2F") {
		t.Errorf("request-target %q does not contain an escaped \"%%2F\" for the embedded \"/\"", gotRequestURI)
	}
}

// TestCheckOrgMembership_EscapedUsernameRoutesToCorrectHandler goes one
// step further than TestCheckOrgMembership_EscapesPathSegments's own
// wire-byte proof: it exercises the actual ROUTE-CONFUSION shape the
// finding describes, against a real segment-aware router (http.ServeMux's
// Go 1.22+ pattern matching, a reasonable stand-in for how GitHub's own
// API router -- or any path-segment-based router -- treats "%2F" as
// distinct from a literal "/"). A malicious username containing "/"
// must still resolve to the SAME, single /orgs/{org}/members/{username}
// route with the full malicious string intact as one path value -- not
// fall through to a different route, a wrong segment count, or a 404.
func TestCheckOrgMembership_EscapedUsernameRoutesToCorrectHandler(t *testing.T) {
	t.Parallel()

	const org = "my-org"
	const maliciousUsername = "evil/other-user"

	var matchedUsername string
	var matched bool

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orgs/{org}/members/{username}", func(w http.ResponseWriter, r *http.Request) {
		matched = true
		matchedUsername = r.PathValue("username")
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	got := checkOrgMembership(t.Context(), client, server.URL, org, maliciousUsername)
	if !got {
		t.Fatal("checkOrgMembership() = false, want true (the escaped request must match the specific route and receive 204)")
	}
	if !matched {
		t.Fatal("the /orgs/{org}/members/{username} route never matched -- the escaped username should route here as a single segment")
	}
	if matchedUsername != maliciousUsername {
		t.Errorf("router observed username = %q, want %q (the embedded \"/\" must survive as data within one path value, not split the route)",
			matchedUsername, maliciousUsername)
	}

	// Contrast: a naive, unescaped request for the exact same inputs must
	// NOT cleanly match this same single-username-segment route -- proving
	// the escaping fix is what makes route resolution correct, not an
	// incidental side effect.
	naiveResp, err := client.Get(server.URL + "/orgs/" + org + "/members/" + maliciousUsername)
	if err != nil {
		t.Fatalf("naive unescaped request: %v", err)
	}
	defer func() { _ = naiveResp.Body.Close() }()
	if naiveResp.StatusCode == http.StatusNoContent {
		t.Error("naive unescaped request unexpectedly matched the single-segment route with 204 -- " +
			"this test no longer demonstrates a real difference between escaped and unescaped routing")
	}
}
