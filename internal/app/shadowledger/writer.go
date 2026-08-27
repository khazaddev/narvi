// This file implements the ledger write itself, with the semantics §30.6
// requires of it: record-or-fail.
//
// A suppressed effect that is not recorded is a contract violation. The
// operator's entire evaluation is the record, so a write the platform
// silently swallowed is worse than one that failed loudly -- and failing
// loudly is safe here in a way it almost never is, because nothing
// external happened. There is no half-applied state to reconcile: the
// caller sees an error, and the customer's repository is untouched either
// way.
//
// So Record returns an error and every caller must propagate it. A gate
// that suppressed a write and then discarded a ledger failure would be
// reporting a suppression it cannot evidence.

package shadowledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// Store is the narrow write surface this package needs, satisfied by
// *postgres.ShadowSCMWriteStore. An interface rather than the concrete
// store so the gate can be tested against a store that fails on demand --
// which is the case that matters, since record-or-fail is the property.
type Store interface {
	Create(ctx context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error)
}

// Entry is one suppressed write, before it becomes a row.
//
// Spec and Result are marshalled to JSON here rather than by the caller,
// so a caller cannot hand over a pre-serialised blob whose contents this
// package never saw -- which is how a token would get in despite the
// spec types in spec.go having no field for one.
type Entry struct {
	// Operation names what was suppressed, in the vocabulary an operator
	// reads -- "create_pr", "merge_pr", "http_post". Not a Go symbol.
	Operation string

	// RepoFullName is "owner/repo". The ledger is read per repository
	// because that is the unit a customer's trust is scoped to.
	RepoFullName string

	// Target is the specific thing acted on where there is one -- a PR
	// number, a branch, a path. Empty where the operation has no single
	// target.
	Target string

	// Spec is one of this package's own token-free spec types.
	Spec any

	// Result is the synthetic result handed back to the caller, or nil
	// where none was synthesized. Nil is the honest value for MergePR
	// (§30.7): the row then records that a merge was suppressed and that
	// nothing was invented in its place.
	Result any

	SessionID     pgtype.UUID
	CorrelationID string
}

// Record writes one suppressed effect and returns an error if it could not
// be written. The caller must not report a suppression it could not
// record -- see this file's own top comment for why that direction is the
// safe one.
func Record(ctx context.Context, store Store, e Entry) error {
	if e.Operation == "" {
		return fmt.Errorf("shadowledger: refusing to record a suppressed write with no operation")
	}
	if e.RepoFullName == "" {
		// Without a repository the row cannot be read by the surface that
		// exists to answer "what would you have done to MY repository",
		// which makes it evidence nobody can find.
		return fmt.Errorf("shadowledger: refusing to record a suppressed %s with no repository", e.Operation)
	}

	specJSON, err := json.Marshal(e.Spec)
	if err != nil {
		return fmt.Errorf("shadowledger: marshal %s spec: %w", e.Operation, err)
	}

	var resultJSON []byte
	if e.Result != nil {
		resultJSON, err = json.Marshal(e.Result)
		if err != nil {
			return fmt.Errorf("shadowledger: marshal %s result: %w", e.Operation, err)
		}
	}

	if _, err := store.Create(ctx, sqlcgen.CreateShadowSCMWriteParams{
		Operation:     e.Operation,
		RepoFullName:  e.RepoFullName,
		Target:        optionalText(e.Target),
		SpecJson:      specJSON,
		ResultJson:    resultJSON,
		SessionID:     e.SessionID,
		CorrelationID: optionalText(e.CorrelationID),
	}); err != nil {
		return fmt.Errorf("shadowledger: record suppressed %s: %w", e.Operation, err)
	}
	return nil
}

// optionalText maps an empty string to a genuine SQL NULL rather than to
// an empty-string row value. "no target" and "a target that is the empty
// string" are different facts, and only one of them is real.
func optionalText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
