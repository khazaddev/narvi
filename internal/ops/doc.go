// Package ops implements Step 77's ("ops: dashboards, alerts, runbooks",
// §5.3) own structural guard: dashboards and alerts are committed to the
// repo as data (deploy/observability/{dashboards,alerts}/*.json), and this
// package is what keeps them honest as the code moves.
//
// # Why this exists
//
// §10-P6 already names the hazard a hand-maintained ops artifact runs
// into: a document describing behavior nothing actually enforces just
// drifts out of sync with the code it claims to describe. For a user
// guide that means aspirational prose; for a dashboard or an alert it is
// worse, because the failure is SILENT — an alert naming a metric the
// code no longer emits (renamed, removed, or never shipped) never fires.
// It looks exactly like a healthy system with nothing wrong, forever,
// which is strictly more dangerous than having no alert at all: an
// operator trusts it precisely because it exists.
//
// # The mechanism
//
// ScanRegisteredInstruments (instruments.go) is a go/ast walk over this
// repo's own Go source — mirroring tools/lint/narvichecks/notimeliteral's
// own established "go/ast.Inspect over every non-test .go file" shape —
// collecting the string-literal instrument name from every real OTel
// metric-instrument registration call (meter.Int64Counter("name", ...)
// and its seven siblings). This is the SAME source of truth production
// code itself resolves against at runtime (otel.Meter(...).Int64Counter),
// read mechanically rather than hand-maintained, so it can never itself
// drift from what the code actually registers.
//
// LoadDashboards/LoadAlerts (schema.go) parse the committed JSON files.
// CheckDrift (drift.go) compares every metric name either file references
// against the registered set and reports one error per unregistered
// reference. drift_test.go wires this into `go test ./...` (and therefore
// `make test`, already a required CI step) against the REAL repo tree and
// the REAL deploy/observability directory — no separate CI job needed,
// and no dashboard/alert can ship naming a metric nothing emits.
package ops
