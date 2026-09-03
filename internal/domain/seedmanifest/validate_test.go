package seedmanifest_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/sandboxsecret"
	"github.com/narvidev/narvi/internal/domain/seedmanifest"
)

func validParticipant() seedmanifest.Participant {
	return seedmanifest.Participant{GitHubID: 12345, Email: "ada@example.test", DisplayName: "Ada Adminsson"}
}

func validAutomation() seedmanifest.Automation {
	return seedmanifest.Automation{
		Name:        "nightly-sweep",
		Repos:       []seedmanifest.RepoTarget{{Name: "widget-app", URL: "https://github.com/example-org/widget-app"}},
		TriggerType: seedmanifest.AutomationTriggerManual,
	}
}

func TestValidate_ValidMinimalManifest(t *testing.T) {
	t.Parallel()

	m := seedmanifest.Manifest{
		Participants: []seedmanifest.Participant{validParticipant()},
		Secrets: []seedmanifest.Secret{
			{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_TOOL_TOKEN", Value: "s3cr3t"},
		},
		Automations: []seedmanifest.Automation{validAutomation()},
		RepoSettings: []seedmanifest.RepoSetting{
			{RepoFullName: "example-org/widget-app", BlockOnHighRisk: boolPtr(true)},
		},
		RWXPreview: []seedmanifest.RWXPreview{
			{RepoFullName: "example-org/widget-app", DispatchKey: "key", EndpointTemplate: "https://preview.example.test/{{id}}", OrgSlug: "example-org"},
		},
	}

	if err := seedmanifest.Validate(m); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_EmptyManifestIsValid(t *testing.T) {
	t.Parallel()
	if err := seedmanifest.Validate(seedmanifest.Manifest{}); err != nil {
		t.Fatalf("Validate(empty) = %v, want nil", err)
	}
}

func TestValidate_Participants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*seedmanifest.Participant)
		wantErr error
	}{
		{"zero github id", func(p *seedmanifest.Participant) { p.GitHubID = 0 }, seedmanifest.ErrEmptyGitHubID},
		{"negative github id", func(p *seedmanifest.Participant) { p.GitHubID = -1 }, seedmanifest.ErrEmptyGitHubID},
		{"empty email", func(p *seedmanifest.Participant) { p.Email = "" }, seedmanifest.ErrEmptyEmail},
		{"blank email", func(p *seedmanifest.Participant) { p.Email = "   " }, seedmanifest.ErrEmptyEmail},
		{"empty display name", func(p *seedmanifest.Participant) { p.DisplayName = "" }, seedmanifest.ErrEmptyDisplayName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := validParticipant()
			tc.mutate(&p)
			err := seedmanifest.Validate(seedmanifest.Manifest{Participants: []seedmanifest.Participant{p}})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_DuplicateParticipantGitHubID(t *testing.T) {
	t.Parallel()
	p1 := validParticipant()
	p2 := validParticipant()
	p2.Email = "different@example.test" // same GitHubID, different email
	err := seedmanifest.Validate(seedmanifest.Manifest{Participants: []seedmanifest.Participant{p1, p2}})
	if !errors.Is(err, seedmanifest.ErrDuplicateGitHubID) {
		t.Fatalf("Validate() = %v, want error wrapping ErrDuplicateGitHubID", err)
	}
}

func TestValidate_DuplicateParticipantEmailCaseInsensitive(t *testing.T) {
	t.Parallel()
	p1 := validParticipant()
	p1.Email = "Ada@Example.test"
	p2 := validParticipant()
	p2.GitHubID = 999999 // different GitHubID, same email modulo case
	p2.Email = "ada@example.test"
	err := seedmanifest.Validate(seedmanifest.Manifest{Participants: []seedmanifest.Participant{p1, p2}})
	if !errors.Is(err, seedmanifest.ErrDuplicateEmail) {
		t.Fatalf("Validate() = %v, want error wrapping ErrDuplicateEmail", err)
	}
}

func TestValidate_Secrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  seedmanifest.Secret
		wantErr error
	}{
		{
			name:    "unknown scope",
			secret:  seedmanifest.Secret{Scope: "environment", Name: "EXAMPLE_TOKEN", Value: "v"},
			wantErr: seedmanifest.ErrInvalidSecretScope,
		},
		{
			name:    "repo scope missing repoFullName",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeRepo, Name: "EXAMPLE_TOKEN", Value: "v"},
			wantErr: seedmanifest.ErrSecretRepoFullNameUnset,
		},
		{
			name:    "repo scope malformed repoFullName",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeRepo, RepoFullName: "not-a-repo", Name: "EXAMPLE_TOKEN", Value: "v"},
			wantErr: seedmanifest.ErrInvalidRepoFullName,
		},
		{
			name:    "global scope sets repoFullName",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, RepoFullName: "example-org/widget-app", Name: "EXAMPLE_TOKEN", Value: "v"},
			wantErr: seedmanifest.ErrSecretRepoFullNameSet,
		},
		{
			name:    "reserved NARVI_ namespace name",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "NARVI_CUSTOM_THING", Value: "v"},
			wantErr: sandboxsecret.ErrNameReservedNarviNamespace,
		},
		{
			name:    "empty value",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_TOKEN", Value: ""},
			wantErr: seedmanifest.ErrEmptySecretValue,
		},
		{
			name:    "NUL byte in value",
			secret:  seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_TOKEN", Value: "abc\x00def"},
			wantErr: seedmanifest.ErrSecretValueHasNULByte,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := seedmanifest.Validate(seedmanifest.Manifest{Secrets: []seedmanifest.Secret{tc.secret}})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_DuplicateSecret(t *testing.T) {
	t.Parallel()
	s := seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_TOKEN", Value: "v1"}
	s2 := s
	s2.Value = "v2" // same (scope, repoFullName, name); different value still collides
	err := seedmanifest.Validate(seedmanifest.Manifest{Secrets: []seedmanifest.Secret{s, s2}})
	if !errors.Is(err, seedmanifest.ErrDuplicateSecret) {
		t.Fatalf("Validate() = %v, want error wrapping ErrDuplicateSecret", err)
	}
}

func TestValidate_Automations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*seedmanifest.Automation)
		wantErr error
	}{
		{"empty name", func(a *seedmanifest.Automation) { a.Name = "" }, seedmanifest.ErrEmptyAutomationName},
		{"no repos", func(a *seedmanifest.Automation) { a.Repos = nil }, nil}, // checked via substring below
		{"unsupported trigger type github", func(a *seedmanifest.Automation) { a.TriggerType = "github" }, nil},
		{"cron trigger missing schedule", func(a *seedmanifest.Automation) { a.TriggerType = seedmanifest.AutomationTriggerCron }, nil},
		{"cronSchedule set on manual trigger", func(a *seedmanifest.Automation) { a.CronSchedule = "*/5 * * * *" }, nil},
		{"invalid path scope", func(a *seedmanifest.Automation) { a.PathScope = []string{""} }, nil},
		{"duplicate env var name", func(a *seedmanifest.Automation) {
			a.EnvVars = []seedmanifest.EnvVar{{Name: "FLAG", Value: "1"}, {Name: "FLAG", Value: "2"}}
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := validAutomation()
			tc.mutate(&a)
			err := seedmanifest.Validate(seedmanifest.Manifest{Automations: []seedmanifest.Automation{a}})
			if err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_ValidCronAutomation(t *testing.T) {
	t.Parallel()
	a := validAutomation()
	a.TriggerType = seedmanifest.AutomationTriggerCron
	a.CronSchedule = "0 3 * * *"
	if err := seedmanifest.Validate(seedmanifest.Manifest{Automations: []seedmanifest.Automation{a}}); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_DuplicateAutomationName(t *testing.T) {
	t.Parallel()
	a1 := validAutomation()
	a2 := validAutomation()
	a2.Repos = []seedmanifest.RepoTarget{{Name: "other-repo", URL: "https://github.com/example-org/other-repo"}}
	err := seedmanifest.Validate(seedmanifest.Manifest{Automations: []seedmanifest.Automation{a1, a2}})
	if !errors.Is(err, seedmanifest.ErrDuplicateAutomationName) {
		t.Fatalf("Validate() = %v, want error wrapping ErrDuplicateAutomationName", err)
	}
}

func TestValidate_RepoSettings(t *testing.T) {
	t.Parallel()

	t.Run("malformed repoFullName", func(t *testing.T) {
		t.Parallel()
		err := seedmanifest.Validate(seedmanifest.Manifest{
			RepoSettings: []seedmanifest.RepoSetting{{RepoFullName: "not-a-repo", BlockOnHighRisk: boolPtr(true)}},
		})
		if !errors.Is(err, seedmanifest.ErrInvalidRepoFullName) {
			t.Fatalf("Validate() = %v, want error wrapping ErrInvalidRepoFullName", err)
		}
	})

	t.Run("duplicate repoFullName", func(t *testing.T) {
		t.Parallel()
		err := seedmanifest.Validate(seedmanifest.Manifest{
			RepoSettings: []seedmanifest.RepoSetting{
				{RepoFullName: "example-org/widget-app", BlockOnHighRisk: boolPtr(true)},
				{RepoFullName: "example-org/widget-app", SentinelAutofixEnabled: boolPtr(false)},
			},
		})
		if !errors.Is(err, seedmanifest.ErrDuplicateRepoFullName) {
			t.Fatalf("Validate() = %v, want error wrapping ErrDuplicateRepoFullName", err)
		}
	})
}

func TestValidate_RWXPreview(t *testing.T) {
	t.Parallel()

	t.Run("missing required field", func(t *testing.T) {
		t.Parallel()
		err := seedmanifest.Validate(seedmanifest.Manifest{
			RWXPreview: []seedmanifest.RWXPreview{{RepoFullName: "example-org/widget-app", DispatchKey: "k", EndpointTemplate: "", OrgSlug: "example-org"}},
		})
		if !errors.Is(err, seedmanifest.ErrEmptyRWXField) {
			t.Fatalf("Validate() = %v, want error wrapping ErrEmptyRWXField", err)
		}
	})

	t.Run("malformed repoFullName", func(t *testing.T) {
		t.Parallel()
		err := seedmanifest.Validate(seedmanifest.Manifest{
			RWXPreview: []seedmanifest.RWXPreview{{RepoFullName: "nope", DispatchKey: "k", EndpointTemplate: "t", OrgSlug: "o"}},
		})
		if !errors.Is(err, seedmanifest.ErrInvalidRepoFullName) {
			t.Fatalf("Validate() = %v, want error wrapping ErrInvalidRepoFullName", err)
		}
	})
}

// TestValidate_AccumulatesMultipleErrors proves Validate reports every
// problem found in one pass (mirrors platform.Load's own "collect every
// error" convention) rather than stopping at the first -- an operator
// fixing a manifest wants the full list at once.
func TestValidate_AccumulatesMultipleErrors(t *testing.T) {
	t.Parallel()
	m := seedmanifest.Manifest{
		Participants: []seedmanifest.Participant{{GitHubID: 0, Email: "", DisplayName: ""}},
		Secrets:      []seedmanifest.Secret{{Scope: "bogus", Name: "", Value: ""}},
	}
	err := seedmanifest.Validate(m)
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Validate() error %v does not support errors.Join unwrapping", err)
	}
	if got := len(joined.Unwrap()); got < 5 {
		t.Errorf("Validate() reported %d errors, want at least 5 (one manifest with many independent mistakes)", got)
	}
}

func boolPtr(b bool) *bool { return &b }
