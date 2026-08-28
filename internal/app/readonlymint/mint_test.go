package readonlymint_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapp"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/readonlymint"
	"github.com/khazaddev/narvi/internal/app/shadowledger"
)

// fakeMinter is a readonlymint.Minter test double -- there is no real
// GitHub App reachable from this environment (internal/adapters/outbound/
// githubapp's own doc.go), so every test here exercises Mint against this
// fake rather than a real GitHub API call.
type fakeMinter struct {
	token githubapp.Token
	err   error

	sawOwner     string
	sawRepoNames []string
}

func (f *fakeMinter) MintInstallationToken(_ context.Context, owner string, repoNames []string) (githubapp.Token, error) {
	f.sawOwner = owner
	f.sawRepoNames = repoNames
	if f.err != nil {
		return githubapp.Token{}, f.err
	}
	return f.token, nil
}

// fakeLedgerStore mirrors internal/app/shadowledger's own fakeStore
// (writer_test.go) and internal/app/shadowscm's own identical fake
// (decorator_test.go) -- a store that either records the insert or fails
// on demand.
type fakeLedgerStore struct {
	rows []sqlcgen.CreateShadowSCMWriteParams
	err  error
}

func (s *fakeLedgerStore) Create(_ context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error) {
	if s.err != nil {
		return sqlcgen.ShadowScmWrite{}, s.err
	}
	s.rows = append(s.rows, arg)
	return sqlcgen.ShadowScmWrite{}, nil
}

func readOnlyToken() githubapp.Token {
	return githubapp.Token{
		Value:       "ghs_fake_read_only_token",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]string{"contents": "read", "metadata": "read"},
	}
}

// TestMint_Success proves a genuinely read-only minted token is returned
// as-is, with nothing recorded into the ledger.
func TestMint_Success(t *testing.T) {
	minter := &fakeMinter{token: readOnlyToken()}
	ledger := &fakeLedgerStore{}

	got, err := readonlymint.Mint(context.Background(), minter, ledger, "acme", []string{"widgets"}, "acme/widgets", "github.com")
	if err != nil {
		t.Fatalf("Mint() error = %v, want nil", err)
	}
	if got.Value != minter.token.Value {
		t.Errorf("Mint() = %+v, want the minter's own token", got)
	}
	if len(ledger.rows) != 0 {
		t.Errorf("ledger recorded %d rows, want 0 for a successful read-only mint", len(ledger.rows))
	}
	if minter.sawOwner != "acme" || len(minter.sawRepoNames) != 1 || minter.sawRepoNames[0] != "widgets" {
		t.Errorf("minter saw owner=%q repoNames=%v, want owner=acme repoNames=[widgets]", minter.sawOwner, minter.sawRepoNames)
	}
}

// TestMint_MinterFailure_NothingRecorded proves a genuine mint failure
// (never reaching a scope decision at all) is surfaced as a plain error,
// distinct from ErrRefusedByScopeCheck, and records nothing -- there is no
// token to refuse.
func TestMint_MinterFailure_NothingRecorded(t *testing.T) {
	minter := &fakeMinter{err: errors.New("simulated GitHub API failure")}
	ledger := &fakeLedgerStore{}

	_, err := readonlymint.Mint(context.Background(), minter, ledger, "acme", []string{"widgets"}, "acme/widgets", "github.com")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error when the underlying mint itself fails")
	}
	var scopeErr *readonlymint.ErrRefusedByScopeCheck
	if errors.As(err, &scopeErr) {
		t.Errorf("Mint() error = %v (*ErrRefusedByScopeCheck), want a plain mint error -- this is not a scope refusal", err)
	}
	if len(ledger.rows) != 0 {
		t.Errorf("ledger recorded %d rows, want 0 -- a mint failure is not a scope-check refusal", len(ledger.rows))
	}
}

// TestMint_ScopeCheckFailure_RefusesAndRecords is §30.4(4)'s own "the mint
// refuses to serve" requirement: a token minted with anything beyond read
// access is never returned, the refusal is recorded into the ledger with
// no token field, and the returned error is *ErrRefusedByScopeCheck.
func TestMint_ScopeCheckFailure_RefusesAndRecords(t *testing.T) {
	overScoped := readOnlyToken()
	overScoped.Permissions = map[string]string{"contents": "write", "metadata": "read"}
	minter := &fakeMinter{token: overScoped}
	ledger := &fakeLedgerStore{}

	got, err := readonlymint.Mint(context.Background(), minter, ledger, "acme", []string{"widgets"}, "acme/widgets", "github.com")
	if err == nil {
		t.Fatal("Mint() error = nil, want a scope-check refusal")
	}
	var scopeErr *readonlymint.ErrRefusedByScopeCheck
	if !errors.As(err, &scopeErr) {
		t.Fatalf("Mint() error = %v, want *readonlymint.ErrRefusedByScopeCheck", err)
	}
	if got.Value != "" {
		t.Errorf("Mint() token = %+v, want the zero value on a scope-check refusal", got)
	}

	if len(ledger.rows) != 1 {
		t.Fatalf("ledger recorded %d rows, want 1", len(ledger.rows))
	}
	row := ledger.rows[0]
	if row.Operation != "scm_credential_mint_refused" {
		t.Errorf("row.Operation = %q, want %q", row.Operation, "scm_credential_mint_refused")
	}
	if row.RepoFullName != "acme/widgets" {
		t.Errorf("row.RepoFullName = %q, want %q", row.RepoFullName, "acme/widgets")
	}
	if row.ResultJson != nil {
		t.Errorf("row.ResultJson = %s, want nil -- nothing was invented in place of the refused token", row.ResultJson)
	}
	if strings.Contains(string(row.SpecJson), overScoped.Value) {
		t.Errorf("row.SpecJson = %s, must never carry the (refused) token value", row.SpecJson)
	}

	var decoded shadowledger.ScmCredentialMintRefused
	if err := json.Unmarshal(row.SpecJson, &decoded); err != nil {
		t.Fatalf("unmarshal row.SpecJson: %v", err)
	}
	if decoded.Host != "github.com" || decoded.GrantedPermissions["contents"] != "write" {
		t.Errorf("decoded spec = %+v, want Host=github.com and GrantedPermissions[contents]=write", decoded)
	}
}

// TestMint_LedgerRecordFailure_IsAHardError proves record-or-fail end to
// end: when the scope check refuses AND the ledger itself cannot record
// that refusal, Mint must return an error that is NOT ErrRefusedByScopeCheck
// -- a caller must be able to tell "refused, evidenced" apart from
// "refused, but this process cannot even prove it," and must never treat
// the latter as an ordinary, safe-to-403 refusal.
func TestMint_LedgerRecordFailure_IsAHardError(t *testing.T) {
	overScoped := readOnlyToken()
	overScoped.Permissions = map[string]string{"contents": "write"}
	minter := &fakeMinter{token: overScoped}
	ledger := &fakeLedgerStore{err: errors.New("simulated ledger write failure")}

	_, err := readonlymint.Mint(context.Background(), minter, ledger, "acme", []string{"widgets"}, "acme/widgets", "github.com")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error when the ledger cannot record the refusal")
	}
	var scopeErr *readonlymint.ErrRefusedByScopeCheck
	if errors.As(err, &scopeErr) {
		t.Errorf("Mint() error = %v (*ErrRefusedByScopeCheck), want a distinct record-failure error", err)
	}
}
