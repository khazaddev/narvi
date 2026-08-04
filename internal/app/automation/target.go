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

// marshalTargets encodes targets for a JSONB column.
func marshalTargets(targets []domainautomation.Target) ([]byte, error) {
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

// unmarshalTargets decodes a JSONB targets column back into
// []domainautomation.Target.
func unmarshalTargets(raw []byte) ([]domainautomation.Target, error) {
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
