package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
)

// CreateInvocation is this Step's own minimal, durable "an invocation now
// exists, fan it out" entry point -- see this package's own doc.go for why
// this is deliberately as small as internal/app/releasereview.Enqueue: a
// fast, cheap, single INSERT, never the real fan-out work itself (that
// happens later, on Engine's own background pump, entirely decoupled from
// whatever caller decided this automation should fire right now).
//
// Step 52 ("automations: triggers & extras", §8.4) owns the actual trigger-
// condition evaluation that decides WHEN to call this; this Step's own
// callers are its integration tests. targets is validated (automation.
// ValidateTargets, §3.5's own "fan-out ≤10" cap) before anything is
// persisted -- an invalid target list returns an error and writes nothing.
func CreateInvocation(ctx context.Context, invocations InvocationCreator, automationID pgtype.UUID, targets []domainautomation.Target) (sqlcgen.AutomationInvocation, error) {
	if err := domainautomation.ValidateTargets(targets); err != nil {
		return sqlcgen.AutomationInvocation{}, fmt.Errorf("automation: create invocation: %w", err)
	}

	targetsJSON, err := MarshalTargets(targets)
	if err != nil {
		return sqlcgen.AutomationInvocation{}, fmt.Errorf("automation: create invocation: %w", err)
	}

	inv, err := invocations.Create(ctx, sqlcgen.CreateAutomationInvocationParams{
		AutomationID: automationID,
		Targets:      targetsJSON,
		TotalRuns:    int32(len(targets)),
	})
	if err != nil {
		return sqlcgen.AutomationInvocation{}, fmt.Errorf("automation: create invocation: insert: %w", err)
	}
	return inv, nil
}

// InvocationCreator is the narrow slice of
// *postgres.AutomationInvocationStore CreateInvocation needs -- mirrors
// internal/app/releasereview's own PendingEnqueuer/OutboxEnqueuer
// precedent: a small, locally-defined interface so a unit test can inject
// a fake with no real DB round trip.
type InvocationCreator interface {
	Create(ctx context.Context, arg sqlcgen.CreateAutomationInvocationParams) (sqlcgen.AutomationInvocation, error)
}
