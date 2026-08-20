// This file (modelcatalog.go) implements GET /api/models -- §8.8's own
// "Catalog" deliverable (§8.8; §8 item 8;
// §29; §25.2). See internal/app/modelcatalog's own doc.go for the full
// "why a compiled-in snapshot, not a live per-sandbox proxy" reasoning.
//
// Available to any AUTHENTICATED user, all 4 roles including viewer --
// gated by the EXISTING authz.ActionViewAnalytics (§13.3 row 1: "everyone,
// including viewer" -- no separate write verb to withhold from a viewer),
// deliberately reused rather than a new Action: this is non-sensitive
// reference data (model names/context windows/cost/reasoning-effort
// variants, never a secret), and a viewer should still be able to see
// what models exist to understand a session's own model/effort choice
// even though §13.3 keeps them read-only everywhere else. Every OTHER
// route in this package renders a real authz.Authorize verdict rather
// than relying on auth.Middleware alone, so this reuses that same
// discipline instead of being the one exception.

package httpapi

import (
	"net/http"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/app/modelcatalog"
	"github.com/khazaddev/narvi/internal/domain/authz"
)

// GetModelCatalog backs GET /api/models.
func GetModelCatalog() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionViewAnalytics, authz.Resource{}) {
			return
		}
		writeJSON(w, http.StatusOK, modelCatalogResponse(modelcatalog.Catalog()))
	}
}

func modelCatalogResponse(providers []modelcatalog.Provider) restdtos.ModelCatalog {
	out := restdtos.ModelCatalog{Providers: make([]restdtos.ModelCatalogProvider, 0, len(providers))}
	for _, p := range providers {
		models := make([]restdtos.ModelCatalogModel, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, restdtos.ModelCatalogModel{
				Id:            m.ID,
				Name:          m.Name,
				ContextWindow: m.ContextWindow,
				ToolCall:      m.ToolCall,
				Reasoning:     m.Reasoning,
				Variants:      m.Variants,
				Cost: restdtos.ModelCatalogCost{
					Input:      m.Cost.Input,
					Output:     m.Cost.Output,
					CacheRead:  restdtos.ModelCatalogCostCacheRead(m.Cost.CacheRead),
					CacheWrite: restdtos.ModelCatalogCostCacheWrite(m.Cost.CacheWrite),
				},
			})
		}
		out.Providers = append(out.Providers, restdtos.ModelCatalogProvider{Id: p.ID, Models: models})
	}
	return out
}
