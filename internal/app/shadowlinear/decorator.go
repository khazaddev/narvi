// This file implements the Decorator itself. See doc.go for why this is a
// client-method decorator (never the transport -- Linear's whole API is
// one POST /graphql endpoint) and why the repository it checks is
// resolved once, at construction, rather than per call.

package shadowlinear

import (
	"context"
	"errors"

	"github.com/khazaddev/narvi/internal/app/shadowledger"
)

// Client is every Linear operation internal/adapters/inbound/linear needs
// synchronously, live or decorated. *linearapi.Client satisfies this
// directly (structural typing, no adapter needed) and is what *Decorator
// wraps as its own live implementation; *Decorator satisfies it too,
// decorating every write.
type Client interface {
	// GetUserEmail is a read, forwarded to the live client unchanged --
	// see doc.go's own "what is decorated, and what is not".
	GetUserEmail(ctx context.Context, accessToken, userID string) (email string, err error)

	// CreateThoughtActivity posts a `thought` Agent Activity -- the
	// immediate acknowledgment, identity-link notice, and busy/stop
	// notices internal/adapters/inbound/linear posts synchronously from
	// its own webhook handler.
	// identityNotice is the identity-link prompt, passed SEPARATELY from
	// body rather than concatenated into it by the caller. Its text
	// contains a live magic-link URL whose nonce is credential-equivalent
	// -- whoever holds it can bind a Linear identity to a Narvi account --
	// and the shadow decorator records body verbatim into a permanent,
	// append-only table. Keeping it out of body is what makes the ledger
	// record structurally unable to carry it: there is no field for it.
	// Empty when this actor is already linked, which is the common case.
	CreateThoughtActivity(ctx context.Context, accessToken, agentSessionID, body, identityNotice string) error

	// CreateResponseActivity posts a `response` Agent Activity -- the
	// SYNCHRONOUS plan-decision-outcome activity (postPlanOutcomeActivity,
	// webhook.go). See doc.go's own note on this method's separate,
	// outbox-delivered call site, which this package does not touch.
	CreateResponseActivity(ctx context.Context, accessToken, agentSessionID, body, identityNotice string) error
}

// Decorator wraps a live Client and suppresses its writes when
// repoFullName is not currently live.
type Decorator struct {
	live         Client
	ledger       shadowledger.Store
	repoFullName string

	// isLive reports whether repoFullName may really be written to right
	// now. Resolved on every call, never cached -- see doc.go's own "why
	// one fixed repository, not a per-call one".
	isLive func(ctx context.Context, repoFullName string) bool
}

// New builds a Decorator. Every argument is required, mirroring
// shadowscm.New/shadowslack.New's identical refusal of a convenience
// default: a pass-through obtained by omission is the failure mode this
// layer exists to remove.
func New(live Client, ledger shadowledger.Store, repoFullName string, isLive func(context.Context, string) bool) (*Decorator, error) {
	if live == nil {
		return nil, errors.New("shadowlinear: a live Client is required")
	}
	if ledger == nil {
		return nil, errors.New("shadowlinear: a ledger is required -- suppression that cannot be recorded is not suppression")
	}
	if repoFullName == "" {
		return nil, errors.New("shadowlinear: a repoFullName is required -- the ledger is read per repository")
	}
	if isLive == nil {
		return nil, errors.New("shadowlinear: an egress resolver is required")
	}
	return &Decorator{live: live, ledger: ledger, repoFullName: repoFullName, isLive: isLive}, nil
}

// This assertion is the tripwire, exactly like shadowscm.Decorator's own:
// a method added to Client stops this line compiling until it is handled
// below, explicitly -- never by embedding Client, which would satisfy the
// compiler for a new method and pass it straight to the live
// implementation, unsuppressed.
var _ Client = (*Decorator)(nil)

func (d *Decorator) record(ctx context.Context, e shadowledger.Entry) error {
	e.RepoFullName = d.repoFullName
	return shadowledger.Record(ctx, d.ledger, e)
}

// GetUserEmail is a read and is forwarded unchanged.
func (d *Decorator) GetUserEmail(ctx context.Context, accessToken, userID string) (string, error) {
	return d.live.GetUserEmail(ctx, accessToken, userID)
}

// CreateThoughtActivity posts the activity, or records that it would have.
func (d *Decorator) CreateThoughtActivity(ctx context.Context, accessToken, agentSessionID, body, identityNotice string) error {
	if d.isLive(ctx, d.repoFullName) {
		return d.live.CreateThoughtActivity(ctx, accessToken, agentSessionID, body, identityNotice)
	}
	return d.record(ctx, shadowledger.Entry{
		Operation: "linear_create_thought_activity",
		Target:    agentSessionID,
		Spec: shadowledger.LinearThoughtActivity{
			AgentSessionID: agentSessionID,
			Body:           body,
			// The notice's TEXT is never recorded -- only that one was
			// appended. It carries a live magic-link nonce, and this row
			// outlives the session in an append-only table.
			IdentityNoticeAppended: identityNotice != "",
		},
	})
}

// CreateResponseActivity posts the activity, or records that it would have.
func (d *Decorator) CreateResponseActivity(ctx context.Context, accessToken, agentSessionID, body, identityNotice string) error {
	if d.isLive(ctx, d.repoFullName) {
		return d.live.CreateResponseActivity(ctx, accessToken, agentSessionID, body, identityNotice)
	}
	return d.record(ctx, shadowledger.Entry{
		Operation: "linear_create_response_activity",
		Target:    agentSessionID,
		Spec: shadowledger.LinearResponseActivity{
			AgentSessionID:         agentSessionID,
			Body:                   body,
			IdentityNoticeAppended: identityNotice != "",
		},
	})
}
