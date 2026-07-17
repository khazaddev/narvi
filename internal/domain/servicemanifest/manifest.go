package servicemanifest

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Criticality is a service's severity classification, carrying the exact
// fatal/warn semantics start.sh already has today (§6.4) -- just
// per-service instead of per-repo (§14.2, verbatim: "criticality: primary |
// secondary carries the same fatal/warn semantics start.sh already has
// today -- just per-service instead of per-repo").
type Criticality string

const (
	// CriticalityPrimary means this service failing to reach readiness is
	// fatal to the whole boot sequence.
	CriticalityPrimary Criticality = "primary"
	// CriticalitySecondary means this service failing to reach readiness
	// is only ever a logged warning; boot continues.
	CriticalitySecondary Criticality = "secondary"
)

// Readiness names exactly one way to decide a service has become ready.
// Exactly one of Port/Health is ever non-nil on a value Validate returned --
// see doc.go for why both-set and neither-set are rejected outright.
type Readiness struct {
	// Port, when set, is the TCP port a caller dials against 127.0.0.1;
	// a successful connection means ready.
	Port *int
	// Health, when set, is the full URL (e.g.
	// "http://127.0.0.1:4000/health") a caller GETs; a 2xx response means
	// ready. See doc.go for why this Step defines the shape as a
	// self-contained URL.
	Health *string
}

// Service is one .narvi/services.yml entry, already validated by Validate.
type Service struct {
	Name string
	Cmd  string
	// Cwd is relative to the repo root; empty means the repo root itself.
	Cwd         string
	Readiness   Readiness
	Criticality Criticality
}

// Manifest is a fully parsed and validated .narvi/services.yml.
type Manifest struct {
	Services []Service
}

// EmptyServicesError is returned by Validate when the manifest is present but
// declares zero services. A present-but-empty services.yml is rejected
// outright (see doc.go / §14.2 scope note) rather than silently treated as
// a no-op: it would otherwise skip the setup.sh/start.sh fallback (the file
// exists) while also supervising nothing -- doing neither, silently.
type EmptyServicesError struct{}

func (e *EmptyServicesError) Error() string {
	return "servicemanifest: services list is empty; a present services.yml must declare at least one service"
}

// MissingFieldError is returned by Validate when a required string field
// (name or cmd) on one service entry is empty. Index identifies the
// offending entry unambiguously even when Name itself is the empty field
// being reported.
type MissingFieldError struct {
	Index int    // 0-based position of the offending entry in the services list
	Name  string // the entry's own name field, verbatim (may itself be empty)
	Field string // "name" or "cmd"
}

func (e *MissingFieldError) Error() string {
	return fmt.Sprintf("servicemanifest: service[%d] (name %q): %s must not be empty", e.Index, e.Name, e.Field)
}

// DuplicateServiceNameError is returned by Validate when two or more service
// entries share the same, non-empty Name.
type DuplicateServiceNameError struct {
	Name string
}

func (e *DuplicateServiceNameError) Error() string {
	return fmt.Sprintf("servicemanifest: duplicate service name %q", e.Name)
}

// InvalidCriticalityError is returned by Validate when a service's
// criticality is not exactly (case-sensitively) "primary" or "secondary" --
// no default, no case-insensitivity, matching sandboxboot's own
// exact-match philosophy (ParseBootMode).
type InvalidCriticalityError struct {
	Name  string
	Value string
}

func (e *InvalidCriticalityError) Error() string {
	return fmt.Sprintf("servicemanifest: service %q: invalid criticality %q: must be exactly %q or %q",
		e.Name, e.Value, CriticalityPrimary, CriticalitySecondary)
}

// InvalidReadinessError is returned by Validate when a service's readiness
// entry sets zero or both of port/health -- see doc.go for why neither
// shape has a reasonable default.
type InvalidReadinessError struct {
	Name   string
	Reason string
}

func (e *InvalidReadinessError) Error() string {
	return fmt.Sprintf("servicemanifest: service %q: invalid readiness: %s", e.Name, e.Reason)
}

// InvalidPortError is returned by Validate when a service's readiness.port is
// set but out of the valid TCP port range (0, 65535].
type InvalidPortError struct {
	Name string
	Port int
}

func (e *InvalidPortError) Error() string {
	return fmt.Sprintf("servicemanifest: service %q: invalid readiness port %d: must be in (0, 65535]", e.Name, e.Port)
}

// InvalidHealthURLError is returned by Validate when a service's
// readiness.health does not parse as an absolute URL (scheme + host both
// present) -- mirrors internal/adapters/outbound/modal/provider.go's own
// url.Parse-based validation shape for InvalidBaseURLError.
type InvalidHealthURLError struct {
	Name  string
	Value string
}

func (e *InvalidHealthURLError) Error() string {
	return fmt.Sprintf(
		"servicemanifest: service %q: invalid readiness health URL %q: must be an absolute URL (scheme and host both present)",
		e.Name, e.Value,
	)
}

// InvalidCwdError is returned by Validate when a service's cwd escapes the
// repo root: an absolute path, or one containing a ".." path segment.
type InvalidCwdError struct {
	Name   string
	Cwd    string
	Reason string
}

func (e *InvalidCwdError) Error() string {
	return fmt.Sprintf("servicemanifest: service %q: invalid cwd %q: %s", e.Name, e.Cwd, e.Reason)
}

// RawManifest is the raw wire shape of a whole .narvi/services.yml
// document, matching §14.2's own example field names verbatim
// (services/name/cmd/cwd/readiness/port/health/criticality). Its `yaml`
// struct tags (and RawService/RawReadiness's below) are for the impure
// caller's own yaml.Unmarshal to use -- a struct tag is just string
// metadata read via reflection, so declaring one here does NOT require
// this package to import an encoding library itself (see doc.go).
// Validate converts an already-decoded RawManifest into the validated
// Manifest/Service/Readiness types above; nothing outside this file ever
// needs the raw shape directly once Validate has run.
type RawManifest struct {
	Services []RawService `yaml:"services"`
}

// RawService is the raw wire shape of one .narvi/services.yml service
// entry, not yet validated -- see RawManifest.
type RawService struct {
	Name        string       `yaml:"name"`
	Cmd         string       `yaml:"cmd"`
	Cwd         string       `yaml:"cwd"`
	Readiness   RawReadiness `yaml:"readiness"`
	Criticality string       `yaml:"criticality"`
}

// RawReadiness is the raw wire shape of one service's readiness entry, not
// yet validated -- see RawManifest.
type RawReadiness struct {
	Port   *int    `yaml:"port"`
	Health *string `yaml:"health"`
}

// Validate validates an already-decoded RawManifest (typically produced by
// a caller's own yaml.Unmarshal against .narvi/services.yml's bytes --
// see internal/sandboxagent/services.Load) fail-fast: the first invalid
// entry's error is returned immediately, no error accumulation. This
// package never reads a file or unmarshals YAML itself (see doc.go); a
// malformed or wrong-shape YAML document is the caller's own
// yaml.Unmarshal error to surface, before Validate is ever reached.
func Validate(raw RawManifest) (Manifest, error) {
	if len(raw.Services) == 0 {
		return Manifest{}, &EmptyServicesError{}
	}

	seen := make(map[string]bool, len(raw.Services))
	services := make([]Service, 0, len(raw.Services))

	for i, s := range raw.Services {
		if s.Name == "" {
			return Manifest{}, &MissingFieldError{Index: i, Name: s.Name, Field: "name"}
		}
		if seen[s.Name] {
			return Manifest{}, &DuplicateServiceNameError{Name: s.Name}
		}
		seen[s.Name] = true

		if s.Cmd == "" {
			return Manifest{}, &MissingFieldError{Index: i, Name: s.Name, Field: "cmd"}
		}

		criticality := Criticality(s.Criticality)
		if criticality != CriticalityPrimary && criticality != CriticalitySecondary {
			return Manifest{}, &InvalidCriticalityError{Name: s.Name, Value: s.Criticality}
		}

		readiness, err := parseReadiness(s.Name, s.Readiness)
		if err != nil {
			return Manifest{}, err
		}

		if err := validateCwd(s.Name, s.Cwd); err != nil {
			return Manifest{}, err
		}

		services = append(services, Service{
			Name:        s.Name,
			Cmd:         s.Cmd,
			Cwd:         s.Cwd,
			Readiness:   readiness,
			Criticality: criticality,
		})
	}

	return Manifest{Services: services}, nil
}

// parseReadiness validates and converts one service's raw wire-shape
// readiness entry, enforcing that exactly one of port/health is set (see
// doc.go).
func parseReadiness(name string, r RawReadiness) (Readiness, error) {
	switch {
	case r.Port == nil && r.Health == nil:
		return Readiness{}, &InvalidReadinessError{
			Name: name, Reason: "exactly one of port or health must be set; neither was",
		}
	case r.Port != nil && r.Health != nil:
		return Readiness{}, &InvalidReadinessError{
			Name: name, Reason: "exactly one of port or health must be set; both were",
		}
	case r.Port != nil:
		if *r.Port <= 0 || *r.Port > 65535 {
			return Readiness{}, &InvalidPortError{Name: name, Port: *r.Port}
		}
		return Readiness{Port: r.Port}, nil
	default: // r.Health != nil
		parsed, err := url.Parse(*r.Health)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Readiness{}, &InvalidHealthURLError{Name: name, Value: *r.Health}
		}
		return Readiness{Health: r.Health}, nil
	}
}

// validateCwd rejects a cwd that could escape the repo root: an absolute
// path, or one containing a ".." path segment. An empty cwd (the repo root
// itself) is always valid.
func validateCwd(name, cwd string) error {
	if cwd == "" {
		return nil
	}
	if path.IsAbs(cwd) {
		return &InvalidCwdError{Name: name, Cwd: cwd, Reason: "must be relative to the repo root, not absolute"}
	}
	if hasDotDotSegment(cwd) {
		return &InvalidCwdError{Name: name, Cwd: cwd, Reason: `must not contain a ".." segment (would escape the repo root)`}
	}
	return nil
}

// hasDotDotSegment reports whether cwd contains ".." as a full path
// segment (split on "/"), not merely as a substring -- so a legitimate
// directory name like "foo..bar" is never rejected, only an actual ".."
// segment. Deliberately re-implemented here rather than importing
// internal/domain/environment's own hasDotDotSegment: that one is shaped
// for gitignore-style glob patterns (it strips a leading "!" negation
// sigil first) -- a different concern from a plain relative directory
// path, which has no such sigil to account for.
func hasDotDotSegment(cwd string) bool {
	for _, segment := range strings.Split(cwd, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}
