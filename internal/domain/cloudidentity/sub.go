package cloudidentity

// subPrefix is the fixed, literal prefix every minted token's own `sub`
// claim carries -- §27.3, verbatim: "sub = a stable, deterministic,
// per-Environment value (narvi:environment:<environment_id>)".
const subPrefix = "narvi:environment:"

// Sub returns the deterministic `sub` claim value for environmentID --
// this Step's own gap-4 resolution, confirmed against the real schema:
// environmentID MUST be environments.id (migrations/
// 000021_environments.up.sql), the table's own immutable UUID primary
// key, stringified -- never a name, because the environments table has no
// name column at all (only id/path_scope/mock_configured/created_at), so
// there is no mutable alternative to confuse this with. Callers (the
// minting handler, and the binding-management REST responses per this
// Step's own gap-4 "surface the exact sub string" requirement) pass the
// already-fetched sessions.environment_id/environments.id string
// straight through -- this function performs no lookup or validation of
// its own beyond the literal string concatenation §27.3 specifies.
func Sub(environmentID string) string {
	return subPrefix + environmentID
}
