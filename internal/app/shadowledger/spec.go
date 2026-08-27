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
