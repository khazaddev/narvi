package seed

import (
	"fmt"
	"strings"
)

// Outcome names what happened (or, under dry-run, what WOULD happen) to
// one manifest entry.
type Outcome string

// The Outcome values a real (non-dry-run) Item can carry, plus their
// "would_*" dry-run twins and OutcomeError for a per-item failure.
const (
	OutcomeCreated     Outcome = "created"
	OutcomeUpserted    Outcome = "upserted"
	OutcomeSkipped     Outcome = "skipped_already_exists"
	OutcomeWouldCreate Outcome = "would_create"
	OutcomeWouldUpsert Outcome = "would_upsert"
	OutcomeWouldSkip   Outcome = "would_skip_already_exists"
	OutcomeError       Outcome = "error"
)

// Item reports the outcome of one manifest entry. Key is a human-
// readable identifier safe to print (a GitHub id, an email, a repo full
// name, a secret's own NAME -- never a secret VALUE, a webhook token, or
// any other credential). Detail is a short, free-text elaboration (e.g.
// "role=member", "already linked to user <uuid>", an error message) --
// same rule: never a credential.
type Item struct {
	Kind    string
	Key     string
	Outcome Outcome
	Detail  string
}

func (it Item) String() string {
	if it.Detail == "" {
		return fmt.Sprintf("[%s] %s: %s", it.Kind, it.Key, it.Outcome)
	}
	return fmt.Sprintf("[%s] %s: %s (%s)", it.Kind, it.Key, it.Outcome, it.Detail)
}

// Report is the full result of one Run call.
type Report struct {
	DryRun bool
	Items  []Item
}

// HasErrors reports whether any Item in r has Outcome == OutcomeError --
// cmd/control-plane's own seed subcommand exits non-zero exactly when
// this is true.
func (r *Report) HasErrors() bool {
	for _, it := range r.Items {
		if it.Outcome == OutcomeError {
			return true
		}
	}
	return false
}

// String renders a plain-text, human-readable summary -- one line per
// Item, grouped in manifest-section order, plus a totals line. Never
// includes a secret value: every Item.Key/Detail this package produces
// is already scrubbed at the point it's constructed (see secrets.go's
// own doc comments), so String needs no redaction logic of its own --
// nothing that would need redacting is ever placed into an Item to begin
// with.
func (r *Report) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("seed: DRY RUN -- no changes were written\n")
	} else {
		b.WriteString("seed: run report\n")
	}

	counts := make(map[Outcome]int)
	for _, it := range r.Items {
		fmt.Fprintf(&b, "  %s\n", it)
		counts[it.Outcome]++
	}

	fmt.Fprintf(&b, "totals: %d item(s)", len(r.Items))
	for _, o := range []Outcome{OutcomeCreated, OutcomeUpserted, OutcomeSkipped, OutcomeWouldCreate, OutcomeWouldUpsert, OutcomeWouldSkip, OutcomeError} {
		if n := counts[o]; n > 0 {
			fmt.Fprintf(&b, ", %d %s", n, o)
		}
	}
	b.WriteString("\n")
	return b.String()
}
