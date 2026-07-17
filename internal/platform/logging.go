// This file (logging.go) implements the structured-logging envelope named
// in the stack-choices line (§1): "log/slog with a structured envelope
// carrying correlation_id, session_id, sandbox_gen." PR-03 scope is the
// mechanism only — NewLogger builds the JSON-handler logger every process
// installs as its default, and Logger(ctx) is the accessor every future PR
// that logs inside a request/session/turn scope is expected to call so the
// three envelope fields ride along automatically. Populating the envelope
// with actual state-transition/routing-decision content (§5.3's "every
// state transition logs from/to/trigger/gen") is domain-PR scope (PR-07+,
// PR-36), not this one.

package platform

import (
	"context"
	"io"
	"log/slog"
)

// NewLogger returns a slog.Logger backed by slog.NewJSONHandler at the
// given level. JSON output is the point: structured logs are what let the
// envelope fields (correlation_id, session_id, sandbox_gen — see Logger)
// be queried/filtered downstream instead of grepped.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// Logger returns slog.Default() enriched via .With(...) with whichever of
// correlation_id, session_id, and sandbox_gen are present on ctx. Only
// attrs actually present on ctx are added — an absent value is omitted
// entirely rather than logged as an empty string or zero.
//
// Convention for every future PR: call platform.Logger(ctx) instead of
// slog.Default() directly anywhere a request/session/turn-scoped context is
// in hand, so the envelope fields ride along on every log line. This is a
// convention, not (yet) enforced by a lint rule.
func Logger(ctx context.Context) *slog.Logger {
	logger := slog.Default()

	if correlationID, ok := CorrelationIDFromContext(ctx); ok {
		logger = logger.With(slog.String("correlation_id", correlationID))
	}
	if sessionID, ok := SessionIDFromContext(ctx); ok {
		logger = logger.With(slog.String("session_id", sessionID))
	}
	if sandboxGen, ok := SandboxGenFromContext(ctx); ok {
		logger = logger.With(slog.Int64("sandbox_gen", sandboxGen))
	}

	return logger
}
