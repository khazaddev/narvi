package automation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cron.go evaluates a standard 5-field schedule at whole-minute
// granularity (CronMatches, below) -- the actual time.Minute literal this
// implies lives in platform.Timeouts.AutomationCronGranularity, never
// here (§5.4/§11: every timeout/interval/duration-unit literal lives in
// platform/timeouts.go, mechanically enforced by tools/lint/narvichecks/
// notimeliteral). app/automation's own trigger pump (triggerpump.go)
// truncates `now` by that SAME named field to derive the CAS-guarded
// "already fired for this bucket" key (automations.last_cron_fired_at,
// migrations/000055_automations_triggers_and_extras.up.sql) -- this file
// itself never needs the literal at all, since CronMatches (below) simply
// compares already-extracted minute/hour/day/month/weekday components,
// never a duration.

// cronField describes one of the five standard cron fields' own valid
// range -- min/max are inclusive, matching the classic five-field vixie-cron
// vocabulary (minute hour day-of-month month day-of-week) this package
// deliberately limits itself to (no seconds field, no "@daily"/"@hourly"
// macros, no day-of-month/day-of-week OR-instead-of-AND special case some
// cron implementations apply when both are restricted) -- a small, honest,
// fully-tested subset rather than a byte-for-byte vixie-cron
// reimplementation, since §3.5/§8.4 name only "cron triggers", never a
// specific dialect to match.
type cronField struct {
	name     string
	min, max int
}

var cronFields = [5]cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12},
	{name: "day-of-week", min: 0, max: 6}, // 0 = Sunday, matching time.Weekday's own numbering.
}

// Sentinel errors ValidateCronExpr/CronMatches can return -- mirrors this
// codebase's own established sentinel-error house style.
var (
	// ErrCronFieldCount means the candidate expression does not split into
	// exactly five whitespace-separated fields.
	ErrCronFieldCount = errors.New("automation: cron expression must have exactly 5 fields (minute hour day-of-month month day-of-week)")
	// ErrCronFieldSyntax means one field failed to parse per this
	// package's own supported grammar (*, N, N-M, */N, N-M/N, or a
	// comma-separated list of any of those).
	ErrCronFieldSyntax = errors.New("automation: invalid cron field syntax")
	// ErrCronFieldRange means a parsed field value fell outside that
	// field's own valid range.
	ErrCronFieldRange = errors.New("automation: cron field value out of range")
)

// InvalidCronExprError reports a candidate cron expression ValidateCronExpr
// rejected, and why -- mirrors environment.InvalidGlobError's own shape.
type InvalidCronExprError struct {
	// Expr is the offending expression, verbatim.
	Expr string
	// Field names which of the 5 fields was rejected ("" when the whole
	// expression's own field COUNT was wrong, ErrCronFieldCount).
	Field string
	// Reason is one of ErrCronFieldCount, ErrCronFieldSyntax, or
	// ErrCronFieldRange -- the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidCronExprError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("automation: invalid cron expression %q: %s", e.Expr, e.Reason)
	}
	return fmt.Sprintf("automation: invalid cron expression %q (field %s): %s", e.Expr, e.Field, e.Reason)
}

func (e *InvalidCronExprError) Unwrap() error { return e.Reason }

// parseCronField parses one raw field against its own cronField definition,
// returning the set of values (within [min, max]) it matches. Supported
// grammar, comma-separated, each item one of:
//
//   - "*"      -- every value in [min, max]
//   - "*/N"    -- every Nth value in [min, max], starting at min
//   - "N"      -- exactly N
//   - "A-B"    -- every value in [A, B] inclusive
//   - "A-B/N"  -- every Nth value in [A, B], starting at A
func parseCronField(raw string, def cronField) (map[int]struct{}, error) {
	values := make(map[int]struct{})

	for _, item := range strings.Split(raw, ",") {
		if item == "" {
			return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
		}

		base := item
		step := 1
		if idx := strings.Index(item, "/"); idx >= 0 {
			base = item[:idx]
			stepStr := item[idx+1:]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
			}
			step = n
		}

		var lo, hi int
		switch {
		case base == "*":
			lo, hi = def.min, def.max
		case strings.Contains(base, "-"):
			parts := strings.SplitN(base, "-", 2)
			var err error
			lo, err = strconv.Atoi(parts[0])
			if err != nil {
				return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
			}
			hi, err = strconv.Atoi(parts[1])
			if err != nil {
				return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
			}
			if lo > hi {
				return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
			}
		default:
			n, err := strconv.Atoi(base)
			if err != nil {
				return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldSyntax}
			}
			lo, hi = n, n
		}

		if lo < def.min || hi > def.max {
			return nil, &InvalidCronExprError{Field: def.name, Reason: ErrCronFieldRange}
		}

		for v := lo; v <= hi; v += step {
			values[v] = struct{}{}
		}
	}

	return values, nil
}

// ValidateCronExpr validates a candidate cron schedule string before it is
// accepted onto an automation's own CronTriggerConfig, at creation/update
// time -- exactly five whitespace-separated fields, each syntactically
// valid and in-range per parseCronField above. Returns the first problem
// found (and stops), same convention as ValidatePathScope/ValidateEnvVars.
func ValidateCronExpr(expr string) error {
	_, err := parseCronExpr(expr)
	return err
}

// parseCronExpr is ValidateCronExpr/CronMatches' own shared parse step --
// splits expr into exactly 5 fields and parses each against cronFields.
func parseCronExpr(expr string) ([5]map[int]struct{}, error) {
	var parsed [5]map[int]struct{}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return parsed, &InvalidCronExprError{Expr: expr, Reason: ErrCronFieldCount}
	}

	for i, raw := range fields {
		values, err := parseCronField(raw, cronFields[i])
		if err != nil {
			var fieldErr *InvalidCronExprError
			if errors.As(err, &fieldErr) {
				return parsed, &InvalidCronExprError{Expr: expr, Field: fieldErr.Field, Reason: fieldErr.Reason}
			}
			return parsed, &InvalidCronExprError{Expr: expr, Reason: err}
		}
		parsed[i] = values
	}

	return parsed, nil
}

// CronMatches reports whether expr's own schedule fires during the whole
// UTC minute containing t -- i.e. whether t's minute/hour/day-of-month/
// month/day-of-week all fall within expr's own parsed fields, standard
// vixie-cron AND semantics across all five fields (no day-of-month/
// day-of-week OR-instead-of-AND special case -- see cronField's own doc
// comment for why). t is always converted to UTC first: a cron schedule is
// evaluated against one fixed, unambiguous timezone, never the caller's
// local one, mirroring this codebase's own established "every persisted
// instant is UTC" convention (TIMESTAMPTZ throughout).
//
// This function is pure (no I/O, no time.Now() -- §11): the caller
// (app/automation's own trigger pump) supplies `now`, exactly like
// IsOrphaned (run.go) already does for the recovery sweep's own injected
// `now`.
func CronMatches(expr string, t time.Time) (bool, error) {
	parsed, err := parseCronExpr(expr)
	if err != nil {
		return false, err
	}

	u := t.UTC()
	if _, ok := parsed[0][u.Minute()]; !ok {
		return false, nil
	}
	if _, ok := parsed[1][u.Hour()]; !ok {
		return false, nil
	}
	if _, ok := parsed[2][u.Day()]; !ok {
		return false, nil
	}
	if _, ok := parsed[3][int(u.Month())]; !ok {
		return false, nil
	}
	if _, ok := parsed[4][int(u.Weekday())]; !ok {
		return false, nil
	}
	return true, nil
}
