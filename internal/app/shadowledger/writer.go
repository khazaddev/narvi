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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// Store is the narrow write surface this package needs, satisfied by
// *postgres.ShadowSCMWriteStore. An interface rather than the concrete
// store so the gate can be tested against a store that fails on demand --
// which is the case that matters, since record-or-fail is the property.
type Store interface {
	Create(ctx context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error)
}

// eventAppender is the optional half of Store: §30.6's own THIRD
// recording write, an `events` row so a suppression shows up inline in
// the session workspace an operator is already watching.
//
// Optional because §30.6 is equally explicit about its status: `events`
// is surface, never durable truth -- it cascades with the session, and
// the shadow_scm_writes row is the record. A store that cannot append
// one still records correctly; the timeline is simply not fed.
//
// It went unbuilt for the whole phase while both §30.6 and the plan row
// said it had shipped, which is why it is wired here, at the one choke
// point every suppression already passes through, rather than at call
// sites that would each have to remember.
type eventAppender interface {
	AppendSuppressionEvent(ctx context.Context, sessionID pgtype.UUID, messageID string, payload []byte) error
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

	// Spec is one of this package's own token-free spec types, and the
	// type is the enforcement: see Spec's own doc comment. A ports spec
	// does not implement it, so a credential cannot be handed to the
	// ledger even by a caller who never thought about credentials.
	Spec Spec

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

	specForJSON, heavyContent := splitHeavyContent(e.Spec)

	specJSON, err := json.Marshal(specForJSON)
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

	row, err := store.Create(ctx, sqlcgen.CreateShadowSCMWriteParams{
		Operation:     e.Operation,
		RepoFullName:  e.RepoFullName,
		Target:        optionalText(e.Target),
		SpecJson:      specJSON,
		ResultJson:    resultJSON,
		SessionID:     e.SessionID,
		CorrelationID: optionalText(e.CorrelationID),
		HeavyContent:  heavyContent,
	})
	if err != nil {
		return fmt.Errorf("shadowledger: record suppressed %s: %w", e.Operation, err)
	}

	appendSuppressionEvent(ctx, store, e, row.ID)
	return nil
}

// appendSuppressionEvent feeds the session timeline, best-effort.
//
// Best-effort here and record-or-fail above are not an inconsistency:
// §30.6 draws exactly that line. The ledger row is the record and a
// suppression that cannot be recorded is a contract violation; the
// `events` row is surface that cascades with the session. Failing the
// whole suppression because a timeline entry did not land would trade the
// durable half for the disposable one.
//
// Skipped entirely for a repo-less suppression: `events` is keyed on a
// session, so there is no timeline for it to appear on.
func appendSuppressionEvent(ctx context.Context, store Store, e Entry, ledgerID pgtype.UUID) {
	appender, ok := store.(eventAppender)
	if !ok || !e.SessionID.Valid {
		return
	}

	// Payload is the ledger id plus a summary, per §30.6 -- deliberately
	// NOT the spec. The spec can carry a customer's file content in full,
	// and `events` is read by a surface with different exposure from the
	// admin-only ledger view.
	payload, err := json.Marshal(struct {
		LedgerID     string `json:"ledgerId"`
		Operation    string `json:"operation"`
		RepoFullName string `json:"repoFullName"`
		Target       string `json:"target,omitempty"`
	}{LedgerID: uuid.UUID(ledgerID.Bytes).String(), Operation: e.Operation, RepoFullName: e.RepoFullName, Target: e.Target})
	if err != nil {
		platform.Logger(ctx).Warn("shadowledger: marshal suppression timeline payload failed", "error", err, "operation", e.Operation)
		return
	}

	if err := appender.AppendSuppressionEvent(ctx, e.SessionID, uuid.NewString(), payload); err != nil {
		platform.Logger(ctx).Warn("shadowledger: append suppression timeline event failed; the ledger row is recorded and is the record (§30.6)",
			"error", err, "operation", e.Operation, "ledger_id", uuid.UUID(ledgerID.Bytes).String())
	}
}

// splitHeavyContent implements the schema-time move migrations/
// 000110_shadow_scm_writes_heavy_content.up.sql exists for: a customer
// repository's own file content -- carried whole on UpdateFileContent,
// per that type's own doc comment -- lives in its OWN column, never
// duplicated into spec_json, so a later retention null-out (§30.9, still
// open) touches exactly one column and leaves every other fact an
// operator's ledger summary reads (operation, repo, target, timestamps)
// completely alone.
//
// This is the ONE place that performs the split -- Record's own single
// choke point, mirroring OutboxStore.Create's own "one column, one
// choke point" precedent (§30.6) -- so every caller of Record still
// constructs an ordinary, complete UpdateFileContent{Content: "..."} the
// same way ports.UpdateFileContentSpec's own real write does; nothing
// upstream of this function needs to know the column split exists.
//
// Returns the spec to marshal into spec_json (an UpdateFileContent has
// its own Content field zeroed, so the two never carry the same bytes
// twice) and the heavy_content column value -- nil for every operation
// that isn't UpdateFileContent, which is the correct "not applicable"
// value, not an empty string standing in for "no content".
func splitHeavyContent(spec Spec) (forJSON Spec, heavyContent *string) {
	ufc, ok := spec.(UpdateFileContent)
	if !ok {
		return spec, nil
	}
	content := ufc.Content
	ufc.Content = ""
	return ufc, &content
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
