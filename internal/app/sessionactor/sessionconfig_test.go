package sessionactor

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// sessionRowWithRepos builds a minimal sqlcgen.Session carrying a real,
// randomly generated id and reposJSON as its raw repos bytes -- enough for
// assembleSessionConfig, which only reads ID/Repos from its sessionRow
// argument.
func sessionRowWithRepos(t *testing.T, reposJSON string) sqlcgen.Session {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(uuid.NewString()); err != nil {
		t.Fatalf("scan uuid: %v", err)
	}
	return sqlcgen.Session{ID: id, Repos: []byte(reposJSON)}
}

// TestPublicWsBaseURL is table-driven over http->ws and https->wss scheme
// derivation, plus an unrecognized-scheme rejection -- the one function
// design decision 6 relies on instead of a second, separately configured
// ws(s):// base URL config field.
func TestPublicWsBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "http to ws", in: "http://localhost:8080", want: "ws://localhost:8080"},
		{name: "https to wss", in: "https://narvi.example.com", want: "wss://narvi.example.com"},
		{name: "https with port", in: "https://narvi.example.com:8443", want: "wss://narvi.example.com:8443"},
		{name: "unrecognized scheme rejected", in: "ftp://example.com", wantErr: true},
		{name: "malformed url rejected", in: "://not-a-url", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := publicWsBaseURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("publicWsBaseURL(%q) error = nil, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("publicWsBaseURL(%q) error = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("publicWsBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReposFromJSON proves the sessions.repos JSONB round-trip: empty/nil
// input yields no repos, and a real repos array unmarshals into the exact
// SessionConfig wire shape.
func TestReposFromJSON(t *testing.T) {
	t.Parallel()

	t.Run("nil input yields no repos", func(t *testing.T) {
		t.Parallel()
		got, err := reposFromJSON(nil)
		if err != nil {
			t.Fatalf("reposFromJSON(nil) error = %v, want nil", err)
		}
		if len(got) != 0 {
			t.Errorf("reposFromJSON(nil) = %+v, want empty", got)
		}
	})

	t.Run("empty array input yields no repos", func(t *testing.T) {
		t.Parallel()
		got, err := reposFromJSON([]byte(`[]`))
		if err != nil {
			t.Fatalf("reposFromJSON([]) error = %v, want nil", err)
		}
		if len(got) != 0 {
			t.Errorf("reposFromJSON([]) = %+v, want empty", got)
		}
	})

	t.Run("real repos array round-trips", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null},{"name":"gadgets","url":"https://github.com/acme/gadgets","branch":"feature/x"}]`)

		got, err := reposFromJSON(raw)
		if err != nil {
			t.Fatalf("reposFromJSON() error = %v, want nil", err)
		}
		if len(got) != 2 {
			t.Fatalf("reposFromJSON() len = %d, want 2", len(got))
		}
		if got[0].Name != "widgets" || got[0].Url != "https://github.com/acme/widgets" || got[0].Branch != nil {
			t.Errorf("repos[0] = %+v, unexpected", got[0])
		}
		if got[1].Name != "gadgets" || got[1].Branch == nil || *got[1].Branch != "feature/x" {
			t.Errorf("repos[1] = %+v, unexpected", got[1])
		}
	})

	t.Run("malformed JSON is a real error", func(t *testing.T) {
		t.Parallel()
		if _, err := reposFromJSON([]byte(`not json`)); err == nil {
			t.Fatal("reposFromJSON(malformed) error = nil, want error")
		}
	})
}

// TestAssembleSessionConfig_BootModeAlwaysFresh proves assembleSessionConfig
// hardcodes BootMode=Fresh (no image-build/snapshot-restore path exists
// yet this Step needs to support) and wires every other field from its own
// inputs correctly, using a minimal Actor constructed directly (no
// Postgres needed -- assembleSessionConfig only reads a.publicBaseURL and
// its own arguments).
func TestAssembleSessionConfig(t *testing.T) {
	t.Parallel()

	a := &Actor{publicBaseURL: "https://narvi.example.com"}

	sessionRow := sessionRowWithRepos(t, `[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`)

	cfg, err := a.assembleSessionConfig(sessionRow, 3, "plaintext-token")
	if err != nil {
		t.Fatalf("assembleSessionConfig() error = %v, want nil", err)
	}

	if cfg.BootMode != sessionconfig.SessionConfigBootModeFresh {
		t.Errorf("BootMode = %q, want %q", cfg.BootMode, sessionconfig.SessionConfigBootModeFresh)
	}
	wantWSURL := "wss://narvi.example.com/sessions/" + sessionRow.ID.String() + "/ws?type=sandbox"
	if cfg.ControlPlaneWsUrl != wantWSURL {
		t.Errorf("ControlPlaneWsUrl = %q, want %q", cfg.ControlPlaneWsUrl, wantWSURL)
	}
	if cfg.CorrelationId != nil {
		t.Errorf("CorrelationId = %v, want nil", cfg.CorrelationId)
	}
	if cfg.Gen != 3 {
		t.Errorf("Gen = %d, want 3", cfg.Gen)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "widgets" {
		t.Errorf("Repos = %+v, unexpected", cfg.Repos)
	}
	if cfg.SandboxToken != "plaintext-token" {
		t.Errorf("SandboxToken = %q, want %q", cfg.SandboxToken, "plaintext-token")
	}
	if cfg.SessionId != sessionRow.ID.String() {
		t.Errorf("SessionId = %q, want %q", cfg.SessionId, sessionRow.ID.String())
	}
}
