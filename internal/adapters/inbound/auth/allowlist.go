package auth

import "strings"

// AllowlistConfig is the signup gate (§13.1: "allowlist of email domains /
// GitHub orgs / explicit users, evaluated at first sign-in"), built once
// from platform.Config at wiring time (cmd/control-plane/main.go). Org
// membership is checked SEPARATELY, inside NewCallbackHandler itself (see
// checkOrgMembership in callback.go) -- it needs a live GitHub API call
// using the signing-in user's own token, not a pure function, so it cannot
// live here.
type AllowlistConfig struct {
	EmailDomains []string
	GitHubOrgs   []string
	Emails       []string
}

// EmailAllowed reports whether email passes the exact-Emails check or the
// EmailDomains-suffix check, both case-insensitive. Does NOT check org
// membership -- see this type's own doc comment.
func (a AllowlistConfig) EmailAllowed(email string) bool {
	lowerEmail := strings.ToLower(email)

	for _, allowed := range a.Emails {
		if strings.EqualFold(allowed, lowerEmail) {
			return true
		}
	}

	at := strings.LastIndex(lowerEmail, "@")
	if at < 0 {
		return false
	}
	domain := lowerEmail[at+1:]
	for _, allowedDomain := range a.EmailDomains {
		if strings.EqualFold(allowedDomain, domain) {
			return true
		}
	}

	return false
}
