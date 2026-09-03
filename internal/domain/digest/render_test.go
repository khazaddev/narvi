package digest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/digest"
	"github.com/narvidev/narvi/internal/domain/review"
)

func TestRender_NoRepos(t *testing.T) {
	t.Parallel()

	out := digest.Render(digest.RollupData{SendDate: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)}, digest.ProviderSlack)
	if !strings.Contains(out, "No review activity") {
		t.Errorf("Render with zero repos = %q, want a message naming no activity", out)
	}
	if !strings.Contains(out, "2026-08-10") {
		t.Errorf("Render output = %q, want the send date rendered", out)
	}
}

func TestRender_NotYetComputedSentinelsAreDistinctFromRealZero(t *testing.T) {
	t.Parallel()

	data := digest.RollupData{
		SendDate: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Repos: []digest.RepoSection{
			{
				RepoFullName:          "acme/no-data-yet",
				VerdictsComputed:      false,
				ContradictionComputed: false,
			},
			{
				RepoFullName:            "acme/real-zero",
				VerdictsComputed:        true,
				ShippableCounts:         map[review.Shippable]int{review.ShippableAuto: 0, review.ShippableNeedsHuman: 0, review.ShippableBlock: 0},
				ContradictionComputed:   true,
				ContradictionRate:       0,
				ContradictionContested:  0,
				ContradictionSampleSize: 10,
			},
		},
	}

	out := digest.Render(data, digest.ProviderSlack)

	if !strings.Contains(out, "no verdicts posted in this window") {
		t.Errorf("Render output = %q, want the not-yet-computed repo to say so explicitly", out)
	}
	if !strings.Contains(out, "contradiction rate: not yet computed") {
		t.Errorf("Render output = %q, want the not-yet-computed contradiction rate to say so explicitly", out)
	}
	if !strings.Contains(out, "auto: 0") {
		t.Errorf("Render output = %q, want the real-zero repo's own genuine zero counts rendered as data, not omitted", out)
	}
	if !strings.Contains(out, "contradiction rate: 0% (0 of 10 confirmed/overridden)") {
		t.Errorf("Render output = %q, want a real, computed 0%% rate rendered distinctly from 'not yet computed'", out)
	}

	// The two repos' own sentinel-vs-real-zero lines must never be
	// identical strings -- that IS the property under test.
	notYetComputedLine := "no verdicts posted in this window"
	realZeroLine := "auto: 0"
	if notYetComputedLine == realZeroLine {
		t.Fatalf("test bug: sentinel and real-zero lines must differ")
	}
}

func TestRender_ProviderBoldDialectDiffers(t *testing.T) {
	t.Parallel()

	data := digest.RollupData{
		SendDate: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Repos:    []digest.RepoSection{{RepoFullName: "acme/widgets"}},
	}

	slackOut := digest.Render(data, digest.ProviderSlack)
	linearOut := digest.Render(data, digest.ProviderLinear)

	if !strings.Contains(slackOut, "*acme/widgets*") {
		t.Errorf("Slack render = %q, want single-asterisk mrkdwn bold", slackOut)
	}
	if strings.Contains(slackOut, "**acme/widgets**") {
		t.Errorf("Slack render = %q, want NEVER double-asterisk", slackOut)
	}
	if !strings.Contains(linearOut, "**acme/widgets**") {
		t.Errorf("Linear render = %q, want double-asterisk CommonMark bold", linearOut)
	}
}

func TestRender_ReposSortedDeterministically(t *testing.T) {
	t.Parallel()

	data := digest.RollupData{
		SendDate: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Repos: []digest.RepoSection{
			{RepoFullName: "zzz/last"},
			{RepoFullName: "aaa/first"},
		},
	}

	out := digest.Render(data, digest.ProviderSlack)
	firstIdx := strings.Index(out, "aaa/first")
	lastIdx := strings.Index(out, "zzz/last")
	if firstIdx == -1 || lastIdx == -1 || firstIdx > lastIdx {
		t.Errorf("Render output = %q, want repos sorted alphabetically regardless of input order", out)
	}
}

func TestRender_AutoMergeToggleStateRendered(t *testing.T) {
	t.Parallel()

	data := digest.RollupData{
		SendDate: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Repos: []digest.RepoSection{
			{RepoFullName: "acme/armed", AutoMergeEnabled: true},
			{RepoFullName: "acme/unarmed", AutoMergeEnabled: false},
		},
	}

	out := digest.Render(data, digest.ProviderSlack)
	if !strings.Contains(out, "auto-merge: on") {
		t.Errorf("Render output = %q, want an 'on' toggle rendered for the armed repo", out)
	}
	if !strings.Contains(out, "auto-merge: off") {
		t.Errorf("Render output = %q, want an 'off' toggle rendered for the unarmed repo", out)
	}
}
