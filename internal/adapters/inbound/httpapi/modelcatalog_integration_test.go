//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

type modelCatalogModelForTest struct {
	Id            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"contextWindow"`
	ToolCall      bool     `json:"toolCall"`
	Reasoning     bool     `json:"reasoning"`
	Variants      []string `json:"variants"`
}

type modelCatalogProviderForTest struct {
	Id     string                     `json:"id"`
	Models []modelCatalogModelForTest `json:"models"`
}

type modelCatalogResponseForTest struct {
	Providers []modelCatalogProviderForTest `json:"providers"`
}

func TestGetModelCatalog_RequiresAuth(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/models", nil, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("GET /api/models with no auth cookie: status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestGetModelCatalog_ViewerCanRead proves §13.3 row 1's own "everyone,
// including viewer" -- unlike /api/me/chatgpt-link, a viewer is NOT
// forbidden here.
func TestGetModelCatalog_ViewerCanRead(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	var got modelCatalogResponseForTest
	status := rig.doJSON(t, http.MethodGet, "/api/models", nil, &got, viewerToken)
	if status != http.StatusOK {
		t.Fatalf("GET /api/models as viewer: status = %d, want %d", status, http.StatusOK)
	}

	byID := make(map[string]modelCatalogProviderForTest, len(got.Providers))
	for _, p := range got.Providers {
		byID[p.Id] = p
	}
	for _, want := range []string{"openai", "anthropic", "google"} {
		p, ok := byID[want]
		if !ok {
			t.Errorf("GET /api/models response has no provider %q", want)
			continue
		}
		if len(p.Models) == 0 {
			t.Errorf("GET /api/models provider %q has zero models", want)
		}
	}
}
