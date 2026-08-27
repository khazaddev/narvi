// This file implements §30.2's layer 1: the typed port decorator.
//
// It exists alongside the transport gate, and the redundancy runs in one
// direction on purpose. The gate below guarantees nothing escapes even
// when this layer's coverage is stale; this layer records with real types
// and keeps the state machines that consume write results coherent, which
// a transport that only sees bytes cannot do.
//
// The decorator implements ports.SourceControl EXPLICITLY -- never by
// embedding the interface. That is the whole point of the file. An
// embedded interface would satisfy the compiler for any method added to
// the port later and pass it straight through to the live implementation,
// which is exactly the silent leak this design exists to prevent. Written
// out method by method, adding a method to the port BREAKS THE BUILD until
// someone decides, deliberately, whether it is a read or a write.
//
// Reads are forwarded untouched: reading a customer's repository leaves no
// trace, and suppressing reads would make the evaluation impossible rather
// than safe.

package shadowscm

import (
	"context"
	"errors"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/shadowledger"
)

// Decorator wraps a live ports.SourceControl and suppresses its writes for
// repositories the resolver reports as shadow.
type Decorator struct {
	live   ports.SourceControl
	ledger shadowledger.Store

	// isLive reports whether repoFullName may really be written to.
	// Resolved per call, never cached: a cached answer keeps suppressing
	// after a promotion and keeps emitting after a demotion.
	isLive func(ctx context.Context, repoFullName string) bool
}

// New builds a Decorator. Every argument is required; there is no
// convenience default that yields a pass-through, because a pass-through
// obtained by omission is the failure mode this layer exists to remove.
func New(live ports.SourceControl, ledger shadowledger.Store, isLive func(context.Context, string) bool) (*Decorator, error) {
	if live == nil {
		return nil, errors.New("shadowscm: a live SourceControl is required")
	}
	if ledger == nil {
		return nil, errors.New("shadowscm: a ledger is required -- suppression that cannot be recorded is not suppression")
	}
	if isLive == nil {
		return nil, errors.New("shadowscm: an egress resolver is required")
	}
	return &Decorator{live: live, ledger: ledger, isLive: isLive}, nil
}

// This assertion is the tripwire, and the reason the implementation is
// written out rather than embedded: a method added to the port stops this
// line compiling until it is handled here.
var _ ports.SourceControl = (*Decorator)(nil)

func repoName(owner, repo string) string { return owner + "/" + repo }

func (d *Decorator) record(ctx context.Context, e shadowledger.Entry) error {
	return shadowledger.Record(ctx, d.ledger, e)
}

func (d *Decorator) CreatePR(ctx context.Context, spec ports.CreatePRSpec) (ports.PRRef, error) {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.CreatePR(ctx, spec)
	}
	synthetic := syntheticPRRef(spec.Owner, spec.Repo)
	if err := d.record(ctx, shadowledger.Entry{
		Operation:    "create_pr",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Target:       spec.Head,
		Spec: shadowledger.CreatePR{
			Owner: spec.Owner, Repo: spec.Repo, Head: spec.Head,
			Base: spec.Base, Title: spec.Title, Body: spec.Body,
		},
		Result: synthetic,
	}); err != nil {
		return ports.PRRef{}, err
	}
	return synthetic, nil
}

func (d *Decorator) UpdateFileContent(ctx context.Context, spec ports.UpdateFileContentSpec) (string, error) {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.UpdateFileContent(ctx, spec)
	}
	if err := d.record(ctx, shadowledger.Entry{
		Operation:    "update_file_content",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Target:       spec.Path,
		Spec: shadowledger.UpdateFileContent{
			Owner: spec.Owner, Repo: spec.Repo, Path: spec.Path,
			Content: spec.Content, SHA: spec.SHA, Branch: spec.Branch, Message: spec.Message,
		},
		Result: syntheticCommitSHA,
	}); err != nil {
		return "", err
	}
	return syntheticCommitSHA, nil
}

func (d *Decorator) UpdatePRBody(ctx context.Context, spec ports.UpdatePRBodySpec) error {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.UpdatePRBody(ctx, spec)
	}
	return d.record(ctx, shadowledger.Entry{
		Operation:    "update_pr_body",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Target:       fmt.Sprintf("%d", spec.Number),
		Spec: shadowledger.UpdatePRBody{
			Owner: spec.Owner, Repo: spec.Repo, Number: spec.Number, Body: spec.Body,
		},
	})
}

func (d *Decorator) RegisterPRStack(ctx context.Context, spec ports.RegisterPRStackSpec) error {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.RegisterPRStack(ctx, spec)
	}
	return d.record(ctx, shadowledger.Entry{
		Operation:    "register_pr_stack",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Spec: shadowledger.RegisterPRStack{
			Owner: spec.Owner, Repo: spec.Repo, PRNumbers: spec.PRNumbers,
		},
	})
}

func (d *Decorator) CreateBranch(ctx context.Context, spec ports.CreateBranchSpec) error {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.CreateBranch(ctx, spec)
	}
	return d.record(ctx, shadowledger.Entry{
		Operation:    "create_branch",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Target:       spec.Branch,
		Spec: shadowledger.CreateBranch{
			Owner: spec.Owner, Repo: spec.Repo, Branch: spec.Branch, SHA: spec.SHA,
		},
	})
}

// MergePR records and then returns ports.ErrShadowSuppressed -- never a
// synthetic merge commit SHA. See that sentinel's own doc comment and
// §30.7 for why this one write refuses a stand-in: an invented SHA becomes
// an audit row and a fake confirmation feeding the very metric whose
// evidence is supposed to justify arming auto-merge for real.
func (d *Decorator) MergePR(ctx context.Context, spec ports.MergePRSpec) (string, error) {
	if d.isLive(ctx, repoName(spec.Owner, spec.Repo)) {
		return d.live.MergePR(ctx, spec)
	}
	if err := d.record(ctx, shadowledger.Entry{
		Operation:    "merge_pr",
		RepoFullName: repoName(spec.Owner, spec.Repo),
		Target:       fmt.Sprintf("%d", spec.Number),
		Spec: shadowledger.MergePR{
			Owner: spec.Owner, Repo: spec.Repo, Number: spec.Number, HeadSHA: spec.HeadSHA,
			MergeMethod: spec.MergeMethod, CommitTitle: spec.CommitTitle, CommitMessage: spec.CommitMessage,
		},
		// Result stays nil: the row records that a merge was suppressed and
		// that nothing was invented in its place.
	}); err != nil {
		return "", err
	}
	return "", ports.ErrShadowSuppressed
}

func (d *Decorator) ResolveBranchSHA(ctx context.Context, spec ports.ResolveBranchSHASpec) (string, string, error) {
	return d.live.ResolveBranchSHA(ctx, spec)
}

func (d *Decorator) ResolveContractsFingerprint(ctx context.Context, spec ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	return d.live.ResolveContractsFingerprint(ctx, spec)
}

func (d *Decorator) CheckRepoAccess(ctx context.Context, spec ports.CheckRepoAccessSpec) (bool, error) {
	return d.live.CheckRepoAccess(ctx, spec)
}

func (d *Decorator) GetFileContent(ctx context.Context, spec ports.GetFileContentSpec) (string, string, bool, error) {
	return d.live.GetFileContent(ctx, spec)
}

func (d *Decorator) GetPRBody(ctx context.Context, owner, repo string, number int, token string) (string, bool, error) {
	return d.live.GetPRBody(ctx, owner, repo, number, token)
}

func (d *Decorator) ListMergedBetween(ctx context.Context, spec ports.ListMergedBetweenSpec) ([]ports.MergedPR, bool, error) {
	return d.live.ListMergedBetween(ctx, spec)
}

func (d *Decorator) ListOpenPRsForUser(ctx context.Context, spec ports.ListOpenPRsForUserSpec) ([]ports.OpenPR, bool, error) {
	return d.live.ListOpenPRsForUser(ctx, spec)
}

func (d *Decorator) GetOpenPR(ctx context.Context, owner, repo string, number int, token string) (ports.OpenPR, bool, error) {
	return d.live.GetOpenPR(ctx, owner, repo, number, token)
}

func (d *Decorator) ResolveCodeOwners(ctx context.Context, spec ports.ResolveCodeOwnersSpec) ([]ports.Owner, error) {
	return d.live.ResolveCodeOwners(ctx, spec)
}
