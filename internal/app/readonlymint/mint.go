package readonlymint

import (
	"context"
	"fmt"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapp"
	"github.com/khazaddev/narvi/internal/app/shadowledger"
	"github.com/khazaddev/narvi/internal/domain/scmscope"
)

// Minter mints a GitHub App installation access token scoped to owner's
// own repoNames -- satisfied in production by
// internal/adapters/outbound/githubapp.Client, and fakeable in tests
// without a real GitHub App (see that package's own doc.go for why no
// real one is reachable from this environment at all).
type Minter interface {
	MintInstallationToken(ctx context.Context, owner string, repoNames []string) (githubapp.Token, error)
}

// ErrRefusedByScopeCheck wraps a scmscope validation failure at mint time
// -- returned by Mint whenever the token GitHub actually granted is not
// read-only, after the refusal has already been durably recorded. A
// caller receiving this error holds no token: Mint never returns a
// non-nil githubapp.Token alongside a non-nil error.
type ErrRefusedByScopeCheck struct {
	Reason string
}

func (e *ErrRefusedByScopeCheck) Error() string {
	return fmt.Sprintf("readonlymint: minted token refused by scope check: %s", e.Reason)
}

// Mint mints one installation token via minter and refuses to return it
// unless internal/domain/scmscope.ValidateReadOnly accepts its granted
// permissions -- §30.4(4)'s own "the mint refuses to serve" requirement.
//
// repoFullName and host are carried through only to shape the ledger
// record on a refusal (shadowledger.Entry.RepoFullName is required
// non-empty, and ScmCredentialMintRefused.Host is the git host the
// caller was minting for) -- Mint itself does not otherwise use them.
//
// Three outcomes:
//  1. minter.MintInstallationToken itself fails (a genuine GitHub API/
//     network failure, never a scope refusal) -> that error, wrapped.
//     Nothing is recorded: no token was ever minted to refuse.
//  2. The minted token's own granted permissions fail scmscope.
//     ValidateReadOnly -> the refusal is recorded into ledger
//     (record-or-fail: a record failure itself becomes the returned
//     error, distinct from ErrRefusedByScopeCheck, so a caller can tell
//     "refused and evidenced" apart from "refused, and this process
//     cannot even prove it") and *ErrRefusedByScopeCheck is returned.
//     The over-scoped token is never returned to the caller.
//  3. Otherwise -> the token, nil error.
func Mint(ctx context.Context, minter Minter, ledger shadowledger.Store, owner string, repoNames []string, repoFullName, host string) (githubapp.Token, error) {
	token, err := minter.MintInstallationToken(ctx, owner, repoNames)
	if err != nil {
		return githubapp.Token{}, fmt.Errorf("readonlymint: mint installation token: %w", err)
	}

	if scopeErr := scmscope.ValidateReadOnly(token.Permissions); scopeErr != nil {
		if recordErr := shadowledger.Record(ctx, ledger, shadowledger.Entry{
			Operation:    "scm_credential_mint_refused",
			RepoFullName: repoFullName,
			Target:       host,
			Spec: shadowledger.ScmCredentialMintRefused{
				Host:               host,
				Reason:             scopeErr.Error(),
				GrantedPermissions: token.Permissions,
			},
		}); recordErr != nil {
			return githubapp.Token{}, fmt.Errorf("readonlymint: record refused mint: %w", recordErr)
		}
		return githubapp.Token{}, &ErrRefusedByScopeCheck{Reason: scopeErr.Error()}
	}

	return token, nil
}
