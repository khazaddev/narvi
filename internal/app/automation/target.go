package automation

import (
	"encoding/json"
	"fmt"

	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
)

// targetJSON is the on-wire shape automations.repos/automation_invocations.
// targets/automation_runs.target are persisted as -- the SAME field names
// (name/url/branch) restdtos.CreateSessionRequestReposElem and sessions.
// repos already use, so a target round-trips through this package with no
// surprising re-keying. Kept as this package's own small, unexported wire
// struct (never internal/domain/automation.Target itself, which stays
// adapter/JSON-independent per §11) -- mirrors internal/app/releasereview's
// own toDomainMergedPR boundary-conversion precedent.
type targetJSON struct {
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Branch *string `json:"branch,omitempty"`
}

// MarshalTargets encodes targets for a JSONB column. Exported (rather than
// kept package-private, unlike this file's own targetJSON wire struct)
// specifically so internal/adapters/inbound/automationwebhook -- a NEW
// inbound package this Step adds, which (unlike internal/adapters/inbound/
// httpapi) has no import-cycle constraint against this package -- can
// decode an automation's own repos JSONB into []domainautomation.Target
// via UnmarshalTargets below without reimplementing this exact wire shape
// a third time.
func MarshalTargets(targets []domainautomation.Target) ([]byte, error) {
	wire := make([]targetJSON, len(targets))
	for i, t := range targets {
		wire[i] = targetJSON{Name: t.Name, URL: t.URL}
		if t.Branch != "" {
			branch := t.Branch
			wire[i].Branch = &branch
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("automation: marshal targets: %w", err)
	}
	return raw, nil
}

// UnmarshalTargets decodes a JSONB targets/repos column back into
// []domainautomation.Target -- see MarshalTargets' own doc comment
// immediately above for why this is exported.
func UnmarshalTargets(raw []byte) ([]domainautomation.Target, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wire []targetJSON
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("automation: unmarshal targets: %w", err)
	}
	targets := make([]domainautomation.Target, len(wire))
	for i, w := range wire {
		t := domainautomation.Target{Name: w.Name, URL: w.URL}
		if w.Branch != nil {
			t.Branch = *w.Branch
		}
		targets[i] = t
	}
	return targets, nil
}
