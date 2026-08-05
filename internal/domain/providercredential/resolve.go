package providercredential

// Candidate is one provider_credentials row's minimal shape Resolve needs
// to pick a winner: which Scope it was configured at, plus an opaque
// payload of whatever type the caller finds useful. Value is generic
// (Candidate[T]) rather than a fixed string specifically so this package
// never has to know or care whether the caller has already decrypted the
// underlying secret or is still carrying an encrypted/opaque row (e.g. a
// full sqlcgen.ProviderCredential, or just an id to decrypt lazily only
// once a winner is known) -- Resolve itself performs no I/O and touches no
// secret material at all (§11: no I/O in /internal/domain), so it must
// never be the thing forcing a decrypt call. The real caller
// (internal/adapters/inbound/httpapi's delivery endpoint) is expected to
// pass Candidate[encryptedRow] and decrypt ONLY the winning row afterward
// -- never decrypting the losing candidates at all, which is both less
// work and strictly less secret material ever touched in memory.
type Candidate[T any] struct {
	Scope Scope
	Value T
}

// Resolve picks the single most-specific Candidate out of candidates,
// per this package's own doc.go ("environment -> repo -> global, most
// specific wins" -- verified against the actual spec text, not the Step
// brief's own reversed paraphrase). candidates may be in any order across
// different Scopes and may freely omit a Scope level entirely (e.g. a
// session with no attached Environment simply never has a ScopeEnvironment
// candidate) -- Resolve tolerates 0, 1, 2, or all 3 levels being present.
//
// When more than one candidate shares the SAME, winning Scope (e.g. a
// session naming 2 repos, each with its own ScopeRepo row) the FIRST one
// encountered in candidates, in input order, wins -- so a caller that
// cares about tie-breaking within one scope level (e.g. "primary repo
// first", §3.4's own "position 0 = primary" convention) controls that by
// the order it builds candidates in, not by anything this function invents
// on its own.
//
// ok is false, and the zero value of T is returned, when candidates is
// empty or contains no Scope this package recognizes (see IsValidScope) --
// an unrecognized Scope is silently ignored rather than causing an error,
// since Resolve has no way to signal one back through its own return
// shape and an unrecognized Scope is a caller bug this package cannot
// itself repair; ValidateScope-style checks belong to the caller
// constructing candidates, not here.
func Resolve[T any](candidates []Candidate[T]) (value T, ok bool) {
	bestPriority := -1
	for _, c := range candidates {
		priority, recognized := scopePriority[c.Scope]
		if !recognized {
			continue
		}
		if bestPriority == -1 || priority < bestPriority {
			bestPriority = priority
			value = c.Value
			ok = true
		}
	}
	return value, ok
}
