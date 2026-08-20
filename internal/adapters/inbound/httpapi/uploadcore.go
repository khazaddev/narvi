// This file (uploadcore.go) holds the pieces §8.6's ("uploads, blob
// storage & the in-sandbox download_file tool", §28) three new
// operations -- mint (uploadmint.go), confirm (uploadconfirm.go), and
// content/download (uploadcontent.go) -- share across BOTH their auth
// variants (sandbox-bearer, outside /api; browser, inside /api and
// auth.Middleware): a small typed error, the sandbox-bearer dead-sandbox/
// gen/token handshake (§28.5's own "scm-credentials' own dead-sandbox/gen
// handshake"), and a couple of tiny pointer-dereference helpers.
//
// sandboxBearerAuth is a genuine, real shared helper -- unlike the FIVE
// pre-existing sandbox-bearer endpoints (scm-credentials, snapshot,
// review/verdict, provider-credentials, workflow/step-outcome), which
// each inline their own byte-for-byte-identical copy of this exact
// sequence (confirmed by direct reading of every one of those files).
// This Step's own three new endpoints share ONE real implementation
// instead of adding three more copies of a sequence that already exists
// five times; retrofitting the five pre-existing ones to also call it is
// deliberately left alone -- an unrelated, larger-blast-radius refactor
// this Step's own scope does not ask for.

package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// uploadError carries the exact (status, message) pair a caller of one of
// this file's sibling cores should surface -- mirrors CreateTurnError's
// own identical purpose (turn.go), reused here as its own small type
// rather than that one directly: CreateTurnError's own doc comment ties it
// specifically to turn-creation's own sentinel-recovery contract
// (Unwrap/ErrPlanAwaitingApproval), which none of the upload endpoints
// need.
type uploadError struct {
	Status  int
	Message string
}

func (e *uploadError) Error() string { return e.Message }

// sandboxBearerAuth performs the dead-sandbox -> gen -> token sequence
// every sandbox-bearer endpoint in this package uses (see this file's own
// top doc comment). Returns the sandbox row and true on success; on
// failure it has already written the appropriate error response
// (404/410/403/401/500) and returns false -- callers should return
// immediately without writing anything further.
func sandboxBearerAuth(w http.ResponseWriter, r *http.Request, sandboxes *postgres.SandboxStore, sessionID pgtype.UUID) (sqlcgen.Sandbox, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	token, ok := bearerTokenFromHeader(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or malformed authorization header")
		return sqlcgen.Sandbox{}, false
	}

	sandboxRow, err := sandboxes.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return sqlcgen.Sandbox{}, false
		}
		logger.Error("httpapi: get sandbox for sandbox-bearer auth failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return sqlcgen.Sandbox{}, false
	}

	// Dead-sandbox check FIRST, before gen/token comparisons -- mirrors
	// scm-credentials.go's own identical ordering.
	if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
		writeError(w, http.StatusGone, "session stopped")
		return sqlcgen.Sandbox{}, false
	}

	presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
	if genErr != nil || presentedGen != int(sandboxRow.Gen) {
		logger.Warn("httpapi: upload endpoint: rejecting: gen mismatch",
			"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
		writeError(w, http.StatusForbidden, "no usable credential for this session")
		return sqlcgen.Sandbox{}, false
	}

	if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
		writeError(w, http.StatusUnauthorized, "invalid sandbox token")
		return sqlcgen.Sandbox{}, false
	}

	return sandboxRow, true
}

// stringOrEmpty dereferences s, or returns "" for a nil pointer -- mirrors
// internal/app/sessionactor/dispatch.go's own identical small helper of
// the same name (a different package, so not directly reusable without
// exporting it there for a single caller here).
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// int64OrZero is stringOrEmpty's own int64 sibling.
func int64OrZero(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
