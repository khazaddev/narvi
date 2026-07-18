package auth

import (
	"net/http"
	"net/http/httptest"
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
