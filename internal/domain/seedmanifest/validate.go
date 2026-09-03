package seedmanifest

import (
	"errors"
	"fmt"
	"strings"

	domainautomation "github.com/narvidev/narvi/internal/domain/automation"
	"github.com/narvidev/narvi/internal/domain/reposource"
	"github.com/narvidev/narvi/internal/domain/sandboxsecret"
)

// Sentinel errors Validate's own per-item helpers wrap into an indexed
// *Invalid*Error (mirroring internal/domain/automation.InvalidEnvVarError's
// own "sentinel + indexed wrapper" house style) -- never a bare
// fmt.Errorf string, so a caller can errors.Is a specific failure kind.
var (
	ErrEmptyGitHubID           = errors.New("seedmanifest: participant githubId must be a positive GitHub user id")
	ErrEmptyEmail              = errors.New("seedmanifest: participant email must not be empty")
	ErrEmptyDisplayName        = errors.New("seedmanifest: participant displayName must not be empty")
	ErrDuplicateGitHubID       = errors.New("seedmanifest: duplicate githubId across participants")
	ErrDuplicateEmail          = errors.New("seedmanifest: duplicate email across participants")
	ErrInvalidSecretScope      = errors.New("seedmanifest: secret scope must be \"global\" or \"repo\"")
	ErrSecretRepoFullNameUnset = errors.New("seedmanifest: repo-scoped secret requires repoFullName")
	ErrSecretRepoFullNameSet   = errors.New("seedmanifest: global-scoped secret must not set repoFullName")
	ErrEmptySecretValue        = errors.New("seedmanifest: secret value must not be empty")
	ErrSecretValueHasNULByte   = errors.New("seedmanifest: secret value must not contain a NUL byte")
	ErrDuplicateSecret         = errors.New("seedmanifest: duplicate secret (scope, repoFullName, name)")
	ErrEmptyAutomationName     = errors.New("seedmanifest: automation name must not be empty")
	ErrDuplicateAutomationName = errors.New("seedmanifest: duplicate automation name")
	ErrInvalidRepoFullName     = errors.New("seedmanifest: repoFullName must be \"owner/repo\" shaped")
	ErrDuplicateRepoFullName   = errors.New("seedmanifest: duplicate repoFullName in this section")
	ErrEmptyRWXField           = errors.New("seedmanifest: rwxPreview requires dispatchKey, endpointTemplate, and orgSlug")
)

// InvalidItemError reports one Section's own Index-th entry as invalid,
// wrapping Reason -- one shape reused across every section below (mirrors
// internal/domain/automation.InvalidEnvVarError's own identical
// "section-scoped indexed wrapper" shape).
type InvalidItemError struct {
	Section string
	Index   int
	Reason  error
}

func (e *InvalidItemError) Error() string {
	return fmt.Sprintf("seedmanifest: %s[%d]: %s", e.Section, e.Index, e.Reason)
}

func (e *InvalidItemError) Unwrap() error { return e.Reason }

// Validate structurally validates m, accumulating every problem found
// (mirrors platform.Load's own "collect every error, never stop at the
// first" convention -- an operator fixing a manifest wants to see every
// mistake in one pass, not one-at-a-time). Returns nil if m is entirely
// well-formed. Never touches the database: a Secret whose (scope,
// repoFullName, name) is well-formed here can still collide with an
// already-seeded row at write time (see internal/app/seed's own
// create-if-absent handling) -- that is a RUNTIME outcome, not a
// structural validation failure.
func Validate(m Manifest) error {
	var errs []error

	errs = append(errs, validateParticipants(m.Participants)...)
	errs = append(errs, validateSecrets(m.Secrets)...)
	errs = append(errs, validateAutomations(m.Automations)...)
	errs = append(errs, validateRepoSettings(m.RepoSettings)...)
	errs = append(errs, validateRWXPreview(m.RWXPreview)...)

	return errors.Join(errs...)
}

func validateParticipants(participants []Participant) []error {
	var errs []error
	seenGitHubID := make(map[int64]int, len(participants))
	seenEmail := make(map[string]int, len(participants))

	for i, p := range participants {
		if p.GitHubID <= 0 {
			errs = append(errs, &InvalidItemError{Section: "participants", Index: i, Reason: ErrEmptyGitHubID})
		}
		if strings.TrimSpace(p.Email) == "" {
			errs = append(errs, &InvalidItemError{Section: "participants", Index: i, Reason: ErrEmptyEmail})
		}
		if strings.TrimSpace(p.DisplayName) == "" {
			errs = append(errs, &InvalidItemError{Section: "participants", Index: i, Reason: ErrEmptyDisplayName})
		}

		if prev, ok := seenGitHubID[p.GitHubID]; ok && p.GitHubID != 0 {
			errs = append(errs, &InvalidItemError{Section: "participants", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateGitHubID, prev)})
		} else {
			seenGitHubID[p.GitHubID] = i
		}

		emailKey := strings.ToLower(strings.TrimSpace(p.Email))
		if prev, ok := seenEmail[emailKey]; ok && emailKey != "" {
			errs = append(errs, &InvalidItemError{Section: "participants", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateEmail, prev)})
		} else if emailKey != "" {
			seenEmail[emailKey] = i
		}
	}
	return errs
}

func validateSecrets(secrets []Secret) []error {
	var errs []error
	type secretKey struct {
		scope        SecretScope
		repoFullName string
		name         string
	}
	seen := make(map[secretKey]int, len(secrets))

	for i, s := range secrets {
		switch s.Scope {
		case SecretScopeGlobal:
			if s.RepoFullName != "" {
				errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: ErrSecretRepoFullNameSet})
			}
		case SecretScopeRepo:
			if s.RepoFullName == "" {
				errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: ErrSecretRepoFullNameUnset})
			} else if _, _, ok := reposource.SplitFullName(s.RepoFullName); !ok {
				errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: fmt.Errorf("%w: %q", ErrInvalidRepoFullName, s.RepoFullName)})
			}
		default:
			errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: fmt.Errorf("%w: %q", ErrInvalidSecretScope, s.Scope)})
		}

		if err := sandboxsecret.ValidateName(s.Name); err != nil {
			errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: err})
		}

		if s.Value == "" {
			errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: ErrEmptySecretValue})
		} else if strings.ContainsRune(s.Value, 0) {
			errs = append(errs, &InvalidItemError{Section: "secrets", Index: i, Reason: ErrSecretValueHasNULByte})
		}

		key := secretKey{scope: s.Scope, repoFullName: s.RepoFullName, name: s.Name}
		if prev, ok := seen[key]; ok {
			errs = append(errs, &InvalidItemError{Section: "secrets", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateSecret, prev)})
		} else {
			seen[key] = i
		}
	}
	return errs
}

func validateAutomations(automations []Automation) []error {
	var errs []error
	seenName := make(map[string]int, len(automations))

	for i, a := range automations {
		if strings.TrimSpace(a.Name) == "" {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: ErrEmptyAutomationName})
		} else if prev, ok := seenName[a.Name]; ok {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateAutomationName, prev)})
		} else {
			seenName[a.Name] = i
		}

		targets := make([]domainautomation.Target, len(a.Repos))
		for j, r := range a.Repos {
			if err := reposource.ValidateRepoName(r.Name); err != nil {
				errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: fmt.Errorf("repos[%d].name: %w", j, err)})
			}
			if err := reposource.ValidateRepoURL(r.URL); err != nil {
				errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: fmt.Errorf("repos[%d].url: %w", j, err)})
			}
			if r.Branch != "" {
				if err := reposource.ValidateBranch(r.Branch); err != nil {
					errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: fmt.Errorf("repos[%d].branch: %w", j, err)})
				}
			}
			targets[j] = domainautomation.Target{Name: r.Name, URL: r.URL, Branch: r.Branch}
		}
		if err := domainautomation.ValidateTargets(targets); err != nil {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: err})
		}

		// This tool supports strictly fewer trigger types than the domain
		// model allows (domainautomation.ValidateTriggerType alone would
		// also accept "github"/"linear") -- see AutomationTriggerType's
		// own doc comment for why those two are out of this Step's scope.
		// Checked separately from (and in addition to) the broader domain
		// validator below, so an operator who writes "github"/"linear"
		// gets this tool's own, more specific error rather than a
		// misleadingly-passing one -- deliberately does NOT skip the
		// independent checks below (sandbox settings, env vars): an
		// unsupported trigger type and an unrelated mistake elsewhere in
		// the same entry should both be reported in one pass.
		switch a.TriggerType {
		case AutomationTriggerManual, AutomationTriggerCron, AutomationTriggerWebhook:
		default:
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i,
				Reason: fmt.Errorf("triggerType %q is not supported by this seed tool (only %q, %q, %q)",
					a.TriggerType, AutomationTriggerManual, AutomationTriggerCron, AutomationTriggerWebhook)})
		}

		triggerType := domainautomation.TriggerType(a.TriggerType)
		if err := domainautomation.ValidateTriggerType(triggerType); err != nil {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: err})
		} else if triggerType == domainautomation.TriggerTypeCron {
			if err := domainautomation.ValidateCronTriggerConfig(domainautomation.CronTriggerConfig{Schedule: a.CronSchedule}); err != nil {
				errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: err})
			}
		} else if a.CronSchedule != "" {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i,
				Reason: fmt.Errorf("cronSchedule set but triggerType is %q, not \"cron\"", a.TriggerType)})
		}

		settings := domainautomation.SandboxSettings{PathScope: a.PathScope, MockConfigured: a.MockConfigured, ContractsPath: a.ContractsPath}
		if err := domainautomation.ValidateSandboxSettings(settings); err != nil {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: err})
		}

		envVars := make([]domainautomation.EnvVar, len(a.EnvVars))
		for j, v := range a.EnvVars {
			envVars[j] = domainautomation.EnvVar{Name: v.Name, Value: v.Value}
		}
		if err := domainautomation.ValidateEnvVars(envVars); err != nil {
			errs = append(errs, &InvalidItemError{Section: "automations", Index: i, Reason: err})
		}
	}
	return errs
}

func validateRepoSettings(settings []RepoSetting) []error {
	var errs []error
	seen := make(map[string]int, len(settings))
	for i, s := range settings {
		if _, _, ok := reposource.SplitFullName(s.RepoFullName); !ok {
			errs = append(errs, &InvalidItemError{Section: "repoSettings", Index: i, Reason: fmt.Errorf("%w: %q", ErrInvalidRepoFullName, s.RepoFullName)})
		}
		if prev, ok := seen[s.RepoFullName]; ok {
			errs = append(errs, &InvalidItemError{Section: "repoSettings", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateRepoFullName, prev)})
		} else {
			seen[s.RepoFullName] = i
		}
	}
	return errs
}

func validateRWXPreview(entries []RWXPreview) []error {
	var errs []error
	seen := make(map[string]int, len(entries))
	for i, e := range entries {
		if _, _, ok := reposource.SplitFullName(e.RepoFullName); !ok {
			errs = append(errs, &InvalidItemError{Section: "rwxPreview", Index: i, Reason: fmt.Errorf("%w: %q", ErrInvalidRepoFullName, e.RepoFullName)})
		}
		if e.DispatchKey == "" || e.EndpointTemplate == "" || e.OrgSlug == "" {
			errs = append(errs, &InvalidItemError{Section: "rwxPreview", Index: i, Reason: ErrEmptyRWXField})
		}
		if prev, ok := seen[e.RepoFullName]; ok {
			errs = append(errs, &InvalidItemError{Section: "rwxPreview", Index: i,
				Reason: fmt.Errorf("%w (also at index %d)", ErrDuplicateRepoFullName, prev)})
		} else {
			seen[e.RepoFullName] = i
		}
	}
	return errs
}
