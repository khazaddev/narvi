package ops

import "fmt"

// GuideDriftError names one documented command in a per-surface user
// guide (docs/guides/*.md) whose binding — a claimed HTTP route, or a
// claimed §18.4 classifier routing-record value — does not actually exist
// in this repo's own source. Kind distinguishes which half of the binding
// failed, for a precise error message; CheckGuideDrift's own doc comment
// enumerates every value.
type GuideDriftError struct {
	Kind    string
	Source  string
	Command string
	Detail  string
}

func (e GuideDriftError) Error() string {
	return fmt.Sprintf("%s %s: command %q: %s", e.Kind, e.Source, e.Command, e.Detail)
}

// CheckGuideDrift is guidedrift's own CheckDrift (drift.go's own
// dashboards/alerts twin, "the same idea over different sources"):
// compares every SurfaceGuide's own claims against a real
// ScanRegisteredRoutes result and a real ScanIntentVocabulary result, and
// returns one GuideDriftError per unregistered reference, in a stable
// (guides in input order, commands in file order within each guide)
// sequence. A nil/empty result means every guide file's own claimed
// surface is real, and every documented command's route or classifier
// binding names something the code actually implements — the CI-passing
// state.
//
// Kind values: "guide-surface" (a guide file's own filename-derived
// surface, e.g. docs/guides/web.md -> "web", is not a registered
// sessions.spawn_source value), "route" (a command's Route string matches
// no ScanRegisteredRoutes entry), "classifier-surface"/"classifier-
// target"/"classifier-mode"/"classifier-source" (a command's Classifier
// field names a Surface/Target/Mode/Source value ScanIntentVocabulary
// never found in source).
//
// What this does NOT catch (validates NAMES, not semantics — the exact
// limitation TestNoMetricDrift's own doc.go already states for the
// dashboards/alerts twin of this check): a route that exists but no
// longer behaves the way the guide's own prose describes, or a
// classifier binding whose Surface/Target/Mode values are each
// individually real but were never actually producible TOGETHER for that
// specific input shape (e.g. nothing stops a guide from claiming
// classifier{surface:"web", target:"review"} even though the web surface
// never calls Classify at all — see docs/guides/README.md's own "what
// this check cannot catch" section for the full list).
func CheckGuideDrift(guides []SurfaceGuide, routes map[string]RegisteredRoute, vocab IntentVocabulary) []GuideDriftError {
	var errs []GuideDriftError

	for _, g := range guides {
		if !vocab.Surfaces[g.Surface] {
			errs = append(errs, GuideDriftError{
				Kind:    "guide-surface",
				Source:  g.SourcePath(),
				Command: g.Title,
				Detail:  fmt.Sprintf("filename-derived surface %q is not a registered sessions.spawn_source value", g.Surface),
			})
		}

		for _, c := range g.Commands {
			if c.Route != "" {
				if _, ok := routes[c.Route]; !ok {
					errs = append(errs, GuideDriftError{
						Kind:    "route",
						Source:  g.SourcePath(),
						Command: c.Name,
						Detail:  fmt.Sprintf("references unregistered route %q", c.Route),
					})
				}
			}

			if c.Classifier != nil {
				cl := c.Classifier
				if !vocab.Surfaces[cl.Surface] {
					errs = append(errs, GuideDriftError{
						Kind:    "classifier-surface",
						Source:  g.SourcePath(),
						Command: c.Name,
						Detail:  fmt.Sprintf("references unregistered surface %q", cl.Surface),
					})
				}
				if cl.Target != "" && !vocab.Targets[cl.Target] {
					errs = append(errs, GuideDriftError{
						Kind:    "classifier-target",
						Source:  g.SourcePath(),
						Command: c.Name,
						Detail:  fmt.Sprintf("references unregistered target %q", cl.Target),
					})
				}
				if cl.Mode != "" && !vocab.Modes[cl.Mode] {
					errs = append(errs, GuideDriftError{
						Kind:    "classifier-mode",
						Source:  g.SourcePath(),
						Command: c.Name,
						Detail:  fmt.Sprintf("references unregistered mode %q", cl.Mode),
					})
				}
				if cl.Source != "" && !vocab.Sources[cl.Source] {
					errs = append(errs, GuideDriftError{
						Kind:    "classifier-source",
						Source:  g.SourcePath(),
						Command: c.Name,
						Detail:  fmt.Sprintf("references unregistered record source %q", cl.Source),
					})
				}
			}
		}
	}

	return errs
}
