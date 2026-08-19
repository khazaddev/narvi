package ops

import "fmt"

// DriftError names one dashboard panel or alert rule referencing a metric
// instrument name the source does not register — the one class of
// problem this Step's own CI drift check exists to catch (doc.go).
type DriftError struct {
	// Kind is "dashboard" or "alert".
	Kind string
	// Source is the file the reference came from (Dashboard.SourcePath /
	// Alert.SourcePath).
	Source string
	// Item is the panel ID or alert Name carrying the bad reference.
	Item string
	// Metric is the unregistered instrument name itself.
	Metric string
}

func (e DriftError) Error() string {
	return fmt.Sprintf("%s %s: %q references unregistered metric %q", e.Kind, e.Source, e.Item, e.Metric)
}

// CheckDrift compares every metric name referenced by dashboards/alerts
// against registered (ScanRegisteredInstruments' own result) and returns
// one DriftError per unregistered reference, in a stable (dashboards
// first, then alerts, each in input order) sequence. A nil/empty result
// means every panel and every alert names only metrics the code actually
// registers — the CI-passing state.
func CheckDrift(dashboards []Dashboard, alerts []Alert, registered map[string]RegisteredInstrument) []DriftError {
	var errs []DriftError
	for _, d := range dashboards {
		for _, p := range d.Panels {
			for _, m := range p.Metrics {
				if _, ok := registered[m]; !ok {
					errs = append(errs, DriftError{Kind: "dashboard", Source: d.SourcePath(), Item: p.ID, Metric: m})
				}
			}
		}
	}
	for _, a := range alerts {
		for _, m := range a.Metrics {
			if _, ok := registered[m]; !ok {
				errs = append(errs, DriftError{Kind: "alert", Source: a.SourcePath(), Item: a.Name, Metric: m})
			}
		}
	}
	return errs
}
