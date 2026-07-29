package intentclassifier

import "testing"

// TestNew_ActiveSurfacesConfigured_LogsBootWarn is the L8 audit fix's own
// required coverage: constructing a Service with a non-empty
// activeSurfaces list must log a boot-time Warn telling an operator that
// IsActive has no production caller yet, so configuring
// NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES currently has zero effect on
// real behavior -- rather than leaving that gap to be discovered the hard
// way.
func TestNew_ActiveSurfacesConfigured_LogsBootWarn(t *testing.T) {
	buf := captureDefaultLoggerJSON(t)

	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, []string{"github", "slack"})
	if svc == nil {
		t.Fatal("New returned nil")
	}

	entries := findLogEntries(t, buf, "intentclassifier: active surfaces configured, but IsActive has no production caller yet -- this setting currently has zero effect on real routing/behavior")
	if len(entries) != 1 {
		t.Fatalf("found %d matching boot-warn log entries, want exactly 1 (full log output: %s)", len(entries), buf.String())
	}
	if entries[0]["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entries[0]["level"])
	}
}

// TestNew_EmptyActiveSurfaces_NoBootWarn is the sibling case: a Service
// built with no configured active surfaces (the default, shadow-only
// configuration) must NOT log the boot warning at all -- there is nothing
// misleading about the operator's config to flag when they never set
// NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES in the first place.
func TestNew_EmptyActiveSurfaces_NoBootWarn(t *testing.T) {
	buf := captureDefaultLoggerJSON(t)

	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), nil, nil)
	if svc == nil {
		t.Fatal("New returned nil")
	}

	if out := buf.String(); out != "" {
		t.Errorf("expected NO log output when activeSurfaces is empty, got: %s", out)
	}
}
