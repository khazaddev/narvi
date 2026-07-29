package intent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// placeholderPattern matches this codebase's own chosen, deliberately
// non-Turing-complete placeholder syntax: "{{variable_name}}" (§18.6 --
// no prior art exists for this piece, designed from scratch this Step).
// A simple named-capture substitution rather than a full templating
// engine with arbitrary logic -- an admin previewing/editing a template
// gets exactly variable substitution, nothing else.
var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// UnknownPlaceholderError is returned by AssembleTemplate/ValidateTemplate
// when a template references one or more placeholders no supplied
// variable/allow-list covers. Names is sorted + de-duplicated for a
// deterministic, testable error message.
type UnknownPlaceholderError struct {
	Names []string
}

func (e *UnknownPlaceholderError) Error() string {
	return fmt.Sprintf("intent: template references unknown placeholder(s): %s", strings.Join(e.Names, ", "))
}

// placeholderNames returns the de-duplicated, sorted set of placeholder
// names referenced anywhere in tmpl.
func placeholderNames(tmpl string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(tmpl, -1)
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		seen[m[1]] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateTemplate fails fast against a typo'd placeholder at template-
// SAVE time (§18.6: "validate/reject unknown placeholders at template-
// save time rather than silently ignoring them -- this is admin-edited
// content, not attacker input, but still deserves a fail-fast validation
// path"). allowedVars is the fixed set of variable names the caller (the
// prompt-template store, at Upsert time) knows this template will
// actually be assembled with. Returns nil iff every placeholder in tmpl
// is a member of allowedVars.
func ValidateTemplate(tmpl string, allowedVars []string) error {
	allowed := make(map[string]struct{}, len(allowedVars))
	for _, v := range allowedVars {
		allowed[v] = struct{}{}
	}

	var unknown []string
	for _, name := range placeholderNames(tmpl) {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return &UnknownPlaceholderError{Names: unknown}
	}
	return nil
}

// AssembleTemplate performs the actual "{{variable_name}}" -> value
// substitution -- the exact final prompt string an admin could preview
// before it is ever sent to the LLM (§18.6). Pure string/data
// substitution only: no conditionals, no loops, no arbitrary code.
//
// Every placeholder present in tmpl must have a corresponding entry in
// vars; AssembleTemplate returns an *UnknownPlaceholderError (never a
// partially-substituted string) when one doesn't, mirroring
// ValidateTemplate's own fail-fast posture -- a missing variable at
// assembly time is exactly as much a caller bug as an unvalidated
// template, so this never silently leaves "{{typo}}" sitting in a prompt
// that then gets sent to the model verbatim.
func AssembleTemplate(tmpl string, vars map[string]string) (string, error) {
	var missing []string
	seen := make(map[string]struct{})
	result := placeholderPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		val, ok := vars[name]
		if !ok {
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				missing = append(missing, name)
			}
			return match
		}
		return val
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", &UnknownPlaceholderError{Names: missing}
	}
	return result, nil
}
