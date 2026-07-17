// Package sessionactor will hold the one-goroutine-plus-mailbox actor per
// active session: hydration, advisory-lock epoch fencing, transactional
// writes, and the named-timer pump — implemented in PR-11 (§2).
package sessionactor
