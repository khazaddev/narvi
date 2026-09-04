package controlplane

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/platform"
)

// healthResponse is the /health JSON body: {"status":"ok"} on success, or
// {"status":"unhealthy"} on failure. The underlying error is logged
// server-side only — pool.Ping's error text can include the DB user,
// database name, and host:port, which an unauthenticated caller has no
// business learning.
type healthResponse struct {
	Status string `json:"status"`
}

// healthHandler backs /health with a real pool.Ping bounded by
// timeouts.HealthCheckTimeout, so a stuck DB reports 503 within that bound
// rather than hanging the handler indefinitely — never panics (Recoverer
// is also mounted above it as a second line of defense), never hangs past
// the timeout.
func healthHandler(pool *pgxpool.Pool, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeouts.HealthCheckTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := pool.Ping(ctx); err != nil {
			slog.Error("health handler: db ping failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "unhealthy"}); encErr != nil {
				slog.Error("health handler: encode unhealthy response", "error", encErr)
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "ok"}); encErr != nil {
			slog.Error("health handler: encode ok response", "error", encErr)
		}
	}
}
