package shadowledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// fakeStore mirrors internal/app/shadowscm/decorator_test.go's own fake of
// the identical name -- a store that either records the insert or fails on
// demand, which is the one property record-or-fail actually needs a test
// to exercise.
type fakeStore struct {
	rows []sqlcgen.CreateShadowSCMWriteParams
	err  error
}

func (s *fakeStore) Create(_ context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error) {
	if s.err != nil {
		return sqlcgen.ShadowScmWrite{}, s.err
	}
	s.rows = append(s.rows, arg)
	return sqlcgen.ShadowScmWrite{}, nil
}

// TestRecord_ScmCredentialMintRefused proves the §30.4(4) refusal record
// round-trips through Record with the SAME record-or-fail semantics every
// other spec in this package already gets, and that the marshalled
// spec_json carries no token -- there is no field to put one in (Spec's
// own doc comment), but this pins that ScmCredentialMintRefused
// specifically, rather than trusting the sealed interface alone to prove
// it for a type nothing here exercises.
func TestRecord_ScmCredentialMintRefused(t *testing.T) {
	store := &fakeStore{}
	err := Record(context.Background(), store, Entry{
		Operation:    "scm_credential_mint_refused",
		RepoFullName: "acme/widgets",
		Target:       "github.com",
		Spec: ScmCredentialMintRefused{
			Host:               "github.com",
			Reason:             `permission "contents" is "write", not read-only`,
			GrantedPermissions: map[string]string{"contents": "write", "metadata": "read"},
		},
	})
	if err != nil {
		t.Fatalf("Record() error = %v, want nil", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("store recorded %d rows, want 1", len(store.rows))
	}

	row := store.rows[0]
	if row.Operation != "scm_credential_mint_refused" {
		t.Errorf("row.Operation = %q, want %q", row.Operation, "scm_credential_mint_refused")
	}
	if row.RepoFullName != "acme/widgets" {
		t.Errorf("row.RepoFullName = %q, want %q", row.RepoFullName, "acme/widgets")
	}
	if row.ResultJson != nil {
		t.Errorf("row.ResultJson = %s, want nil (nothing was invented in place of a refused mint)", row.ResultJson)
	}

	var decoded ScmCredentialMintRefused
	if err := json.Unmarshal(row.SpecJson, &decoded); err != nil {
		t.Fatalf("unmarshal row.SpecJson: %v", err)
	}
	if decoded.Host != "github.com" || decoded.GrantedPermissions["contents"] != "write" {
		t.Errorf("decoded spec = %+v, want the original ScmCredentialMintRefused", decoded)
	}
	if strings.Contains(string(row.SpecJson), "ghs_") || strings.Contains(string(row.SpecJson), "token") {
		t.Errorf("row.SpecJson = %s, must never carry anything token-shaped", row.SpecJson)
	}
}

// TestRecord_FailsLoudlyWhenTheStoreCannotWrite is the record-or-fail
// property itself (this package's own top doc comment): a suppression
// (or, here, a refusal) that cannot be recorded must surface as an error,
// never be reported as if it succeeded.
func TestRecord_FailsLoudlyWhenTheStoreCannotWrite(t *testing.T) {
	store := &fakeStore{err: errors.New("boom: connection reset")}
	err := Record(context.Background(), store, Entry{
		Operation:    "scm_credential_mint_refused",
		RepoFullName: "acme/widgets",
		Spec:         ScmCredentialMintRefused{Host: "github.com", Reason: "test"},
	})
	if err == nil {
		t.Fatal("Record() error = nil, want an error when the store itself fails to write")
	}
}
