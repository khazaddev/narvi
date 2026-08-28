package githubapp

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/oidcsigning"
)

// appJWTClaims is the exact claim set GitHub's own App-authentication JWT
// requires: "iat" (issued-at, backdated by appJWTClockSkewBudget), "exp"
// (at most 10 minutes past "iat" -- bounded here by jwtTTL, which
// Client.New's own caller sets to GitHubAppJWTTTL, comfortably under that
// ceiling), and "iss" (the App's own numeric id).
type appJWTClaims struct {
	IssuedAt  int64 `json:"iat"`
	ExpiresAt int64 `json:"exp"`
	Issuer    int64 `json:"iss"`
}

// signAppJWT builds and signs a fresh App-authentication JWT, valid for
// jwtTTL from now (plus the backdated clock-skew budget). A fresh JWT is
// minted for every call rather than cached and reused across the client's
// own lifetime: unlike an installation access token (§30.4's own "short-
// lived and auto-refreshed" credential, ~1h, reused across a session's own
// git operations via the sandbox-side cache this Step's own purge
// requirements govern), this JWT authenticates AS THE APP for a single,
// immediate REST call and is never handed to anything outside this
// package -- there is no cache to purge, and minting a fresh one each time
// is cheap (one RSA signature, no network call).
func signAppJWT(appID int64, privateKey *rsa.PrivateKey, jwtTTL, clockSkew time.Duration, now time.Time) (string, error) {
	claims := appJWTClaims{
		IssuedAt:  now.Add(-clockSkew).Unix(),
		ExpiresAt: now.Add(jwtTTL).Unix(),
		Issuer:    appID,
	}
	token, err := oidcsigning.Sign(privateKey, "", claims)
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return token, nil
}
