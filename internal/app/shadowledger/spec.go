// This file holds the ledger's own copies of every SCM write spec, and the
// reason they are copies rather than the port's own types.
//
// §30.6: "Specs enter through record types that carry no Token field ...
// excluding the credential from the ledger is a compile error, not a
// redaction pass." Every spec in internal/app/ports carries a plaintext
// Token, because every real write needs one. If the gate recorded those
// types directly, keeping the credential out of the ledger would be a
// discipline -- a redaction step someone must remember, in a codebase
// whose own egress inventory shows exactly that kind of discipline
// accreting and failing.
//
// So the ledger accepts these types instead. They mirror the port's specs
// field for field with one deliberate omission, and the omission is the
// whole design: there is no field to put a token in. A caller cannot pass
// a ports spec where one of these is expected, and converting one to the
// other means naming each field, at which point the token has nowhere to
// go. The compiler enforces what a redaction pass would only promise.
//
// These are also what gets marshalled into shadow_scm_writes.spec_json, so
// the column's contents are bounded by the types rather than by a filter.

package shadowledger

// Spec is what may be recorded as a suppressed write's intention, and it
// is a SEALED interface: the unexported method means only this package's
// own types can satisfy it. Nothing outside can add a new one, and --
// this is the point -- nothing outside can pass a ports spec here, because
// every one of those carries a plaintext Token and none of them implements
// this method.
//
// The first version of this file declared the token-free types and then
// accepted them through a field typed `any`. That made the exclusion a
// convention: the right types existed, and a caller could hand over
// ports.MergePRSpec instead and marshal a live credential straight into
// the column. Verified by writing that call and watching "ghp_..." land in
// spec_json. §30.6 asks for a compile error, and `any` cannot be one.
type Spec interface {
	isShadowSpec()
}

func (CreatePR) isShadowSpec()                 {}
func (UpdateFileContent) isShadowSpec()        {}
func (UpdatePRBody) isShadowSpec()             {}
func (RegisterPRStack) isShadowSpec()          {}
func (CreateBranch) isShadowSpec()             {}
func (MergePR) isShadowSpec()                  {}
func (Transport) isShadowSpec()                {}
func (ScmCredentialMintRefused) isShadowSpec() {}
func (ScmCredentialSubstituted) isShadowSpec() {}
func (SlackAck) isShadowSpec()                 {}
func (SlackIdentityLinkNotice) isShadowSpec()  {}
func (SlackEphemeral) isShadowSpec()           {}
func (SlackMessageUpdate) isShadowSpec()       {}
func (SlackViewOpen) isShadowSpec()            {}
func (LinearThoughtActivity) isShadowSpec()    {}
func (LinearResponseActivity) isShadowSpec()   {}
func (Push) isShadowSpec()                     {}

// CreatePR mirrors ports.CreatePRSpec without its Token.
type CreatePR struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// UpdateFileContent mirrors ports.UpdateFileContentSpec without its Token.
//
// Content is carried in full rather than summarised: the operator's whole
// question in shadow is "what would this have written into my repository",
// and a length or a hash does not answer it.
type UpdateFileContent struct {
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA     string `json:"sha"`
	Branch  string `json:"branch"`
	Message string `json:"message"`
}

// UpdatePRBody mirrors ports.UpdatePRBodySpec without its Token.
type UpdatePRBody struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Body   string `json:"body"`
}

// RegisterPRStack mirrors ports.RegisterPRStackSpec without its Token.
type RegisterPRStack struct {
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	PRNumbers []int  `json:"prNumbers"`
}

// CreateBranch mirrors ports.CreateBranchSpec without its Token.
type CreateBranch struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// MergePR mirrors ports.MergePRSpec without its Token.
//
// A merge is recorded like every other suppressed write, but unlike the
// other five it gets no synthetic result: §30.7 is explicit that a
// fabricated merge success is a false-record generator rather than a
// stand-in, so the ledger row for a merge carries a spec and a NULL
// result.
type MergePR struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	HeadSHA       string `json:"headSha"`
	MergeMethod   string `json:"mergeMethod"`
	CommitTitle   string `json:"commitTitle"`
	CommitMessage string `json:"commitMessage"`
}

// Transport is what the transport gate records when it intercepts a
// mutating request the typed layer never saw -- a method outside the port,
// a synchronous comment posted by an ingress handler, or a method added to
// the concrete adapter after this was written (§30.2 layer 0).
//
// It carries the request's decoded intention rather than a typed spec,
// because the whole point of that layer is that it does not need to know
// the shape of what it is stopping. Body is the request payload as sent.
// There is no token field here either, and the gate never copies the
// Authorization header into it.
type Transport struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Host   string `json:"host"`
	Body   string `json:"body"`
}

// ScmCredentialMintRefused is what internal/adapters/inbound/httpapi.
// ScmCredentials records when §30.4(4)'s own fail-closed scope check
// refuses to hand back a credential it just minted -- the read-only
// GitHub App installation token this Step's shadow-substitution branch
// requested came back carrying a permission this codebase never asked
// for and will not trust, so no credential is served at all. Like every
// other spec in this file, it carries no token: only what was requested
// and what GitHub actually reported granting, both already redacted of
// any secret value by the time they reach here (a permission level is
// the string "read"/"write"/"admin", never a credential).
type ScmCredentialMintRefused struct {
	// Host is the git host the sandbox requested a credential for.
	Host string `json:"host"`
	// Reason is the scmscope validation failure's own Error() text --
	// names the offending permission and level, e.g. `permission
	// "contents" is "write", not read-only`.
	Reason string `json:"reason"`
	// GrantedPermissions is exactly what GitHub's own mint response
	// reported granting -- the same map internal/domain/scmscope.
	// ValidateReadOnly rejected.
	GrantedPermissions map[string]string `json:"grantedPermissions"`
}

// ScmCredentialSubstituted is §30.6's own "the shadow mint records its
// substitution" -- the SUCCESS counterpart to ScmCredentialMintRefused,
// written every time a write-capable credential is replaced by a
// read-only installation token.
//
// It is the more important of the two records, and it was the one
// missing. A refusal is loud on its own: the sandbox gets a 403 and
// cannot proceed. A successful substitution is silent by construction --
// the sandbox receives a credential, clones happily, and discovers the
// missing write capability only if it tries to push. Without this row the
// ledger cannot answer the question it exists for, "what did shadow mode
// actually suppress on this session", for the single most consequential
// suppression on the credential path.
//
// Like every spec here it carries no token -- not even a prefix or a
// length. What was substituted is a fact about capability, and the
// substituted token's own value is never evidence of anything.
type ScmCredentialSubstituted struct {
	// Host is the git host the sandbox requested a credential for.
	Host string `json:"host"`
	// Owner and RepoNames are the scope the read-only installation token
	// was minted FOR -- the same values handed to GitHub's mint call.
	Owner     string   `json:"owner"`
	RepoNames []string `json:"repoNames"`
	// GrantedPermissions is what GitHub reported granting, after
	// internal/domain/scmscope.ValidateReadOnly accepted it. Recording
	// the accepted set (not just the rejected one) is what makes the
	// ledger sufficient to audit a substitution after the fact.
	GrantedPermissions map[string]string `json:"grantedPermissions"`
}

// SlackAck mirrors internal/app/shadowslack's PostAck argument list --
// §30.3's family 2 (the in-thread chat.postMessage ack, formerly internal/
// adapters/inbound/slack/ack.go's own private ackClient). No token field:
// every Slack call in this codebase is authenticated with the ONE
// configured bot token, never a per-call credential, so there is nothing
// here to exclude in the first place -- unlike the GitHub specs above,
// whose whole point is a field that ISN'T there.
type SlackAck struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"threadTs"`
	Text     string `json:"text"`
}

// SlackEphemeral mirrors chat.postEphemeral's own argument list -- §30.3's
// family 3 (interactive.go's identity-link/denial notices) and the
// identity-link notice handler.go posts on its own Events-API path.
type SlackEphemeral struct {
	Channel  string `json:"channel"`
	UserID   string `json:"userId"`
	ThreadTS string `json:"threadTs"`
	Text     string `json:"text"`
}

// SlackMessageUpdate mirrors chat.update's own argument list -- §30.3's
// family 3, interactive.go's own plan-decision-outcome rewrite of the
// original approval message.
type SlackMessageUpdate struct {
	Channel string `json:"channel"`
	Ts      string `json:"ts"`
	Text    string `json:"text"`
}

// SlackViewOpen mirrors views.open's own argument list -- §30.3's family
// 3, interactive.go's own "Request changes" feedback modal. TriggerID is
// Slack's own short-lived interaction token, not a Narvi credential; it is
// already expired by the time any operator could read this row.
type SlackViewOpen struct {
	TriggerID string `json:"triggerId"`
	PlanID    string `json:"planId"`
	SessionID string `json:"sessionId"`
}

// LinearThoughtActivity mirrors linearapi.Client.CreateThoughtActivity's
// own argument list, minus accessToken -- §30.3's family 4. Linear's own
// per-workspace installation token is exactly the kind of secret §30.6
// requires excluded from every spec in this file, same as GitHub's above,
// even though Linear's token is not itself an SCM write credential.
type LinearThoughtActivity struct {
	AgentSessionID string `json:"agentSessionId"`
	Body           string `json:"body"`
}

// LinearResponseActivity mirrors linearapi.Client.CreateResponseActivity's
// own argument list, minus accessToken -- §30.3's family 4 (the
// synchronous plan-decision-outcome activity; the turn-outcome
// CreateResponseActivity call is a SEPARATE, outbox-delivered path already
// covered by the outbox's own §30.2 classification, not this decorator).
type LinearResponseActivity struct {
	AgentSessionID string `json:"agentSessionId"`
	Body           string `json:"body"`
}

// SlackIdentityLinkNotice records that an identity-link prompt would have
// been shown, and DELIBERATELY CARRIES NO TEXT.
//
// The notice's body contains a live magic-link URL whose nonce is
// credential-equivalent: whoever holds it can bind a Slack identity to a
// Narvi account. §30.6 requires the ledger's record types to exclude
// credentials "a compile error, not a redaction pass" -- and the sealed
// marker alone does not deliver that here, because this secret does not
// arrive in a field named Token. It arrives inside human-readable text.
//
// So the exclusion is the type's SHAPE. There is no field to put the
// notice in, which is why the caller has a distinct method rather than
// passing this text to the general ephemeral recorder. Redacting the URL
// out of a text field would be exactly the redaction pass §30.6 rejects:
// it works until someone changes the notice's wording.
//
// An evaluator loses nothing that matters. "An unlinked user was prompted
// to link their account, in this channel" is the whole fact; the URL is a
// per-user secret, not evidence about the workspace.
type SlackIdentityLinkNotice struct {
	// Channel is where the ephemeral would have appeared.
	Channel string `json:"channel"`
	// UserID is the only person who would have seen it.
	UserID string `json:"userId"`
	// ThreadTS is the thread it would have been scoped to, if any.
	ThreadTS string `json:"threadTs"`
}

// Push is what sendPushBestEffort (internal/app/sessionactor/pushpr.go)
// records when a turn's own frozen push/PR decision (§30.8) says shadow --
// §30.9's resolved mirror decision (no git mirror; short-circuit the push
// itself, done properly) means the sandbox WS push command is never sent
// at all, for a repository whose credential could not have pushed it
// anyway (§30.4: the read-only token and the UID boundary are the
// security here, never this gate).
//
// With the push command never sent, no push_complete (or push_error)
// ever arrives on the wire, so createPRBestEffort's own "would have
// opened PR ..." row (§30.7) never gets a turn to run either -- a turn
// that reaches this point always completed successfully and always names
// at least one repo (pushpr.go's own completeProcessingTurn only ever
// produces a pushSignal for that case), so a real push would always have
// been followed by a real CreatePR attempt next. This ONE row records
// both facts together, since nothing downstream ever will.
type Push struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// WouldOpenPR is always true -- see this type's own doc comment for
	// why. Recorded explicitly, not left implied, so a reader of this row
	// alone sees the whole suppressed sequence, not just its first half.
	WouldOpenPR bool `json:"wouldOpenPr"`
}

// These assertions pin the sealed set. Removing isShadowSpec from any of
// them, or forgetting it on a type added later, is a build failure at the
// point where someone would otherwise have widened the ledger's input
// silently.
var (
	_ Spec = CreatePR{}
	_ Spec = UpdateFileContent{}
	_ Spec = UpdatePRBody{}
	_ Spec = RegisterPRStack{}
	_ Spec = CreateBranch{}
	_ Spec = MergePR{}
	_ Spec = Transport{}
	_ Spec = ScmCredentialMintRefused{}
	_ Spec = ScmCredentialSubstituted{}
	_ Spec = SlackAck{}
	_ Spec = SlackEphemeral{}
	_ Spec = SlackIdentityLinkNotice{}
	_ Spec = SlackMessageUpdate{}
	_ Spec = SlackViewOpen{}
	_ Spec = LinearThoughtActivity{}
	_ Spec = LinearResponseActivity{}
	_ Spec = Push{}
)
