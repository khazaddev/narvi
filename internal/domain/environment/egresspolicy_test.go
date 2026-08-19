package environment

import (
	"errors"
	"reflect"
	"testing"
)

func TestValidateEgressPolicy(t *testing.T) {
	tests := []struct {
		name    string
		policy  EgressPolicy
		wantErr error
	}{
		{name: "zero value is valid (unconfigured)", policy: EgressPolicy{}},
		{name: "open mode, empty allowlist is valid", policy: EgressPolicy{Mode: EgressModeOpen}},
		{name: "allowlist mode with hosts is valid", policy: EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"example.com"}}},
		{name: "allowlist mode with empty allowlist is valid (floor still applies later)", policy: EgressPolicy{Mode: EgressModeAllowlist}},
		{
			name:    "unconfigured mode with a non-empty allowlist is rejected",
			policy:  EgressPolicy{Allowlist: []string{"example.com"}},
			wantErr: ErrEgressPolicyAllowlistOnOpen,
		},
		{
			name:    "open mode with a non-empty allowlist is rejected",
			policy:  EgressPolicy{Mode: EgressModeOpen, Allowlist: []string{"example.com"}},
			wantErr: ErrEgressPolicyAllowlistOnOpen,
		},
		{
			name:    "invalid mode is rejected",
			policy:  EgressPolicy{Mode: "closed"},
			wantErr: ErrEgressPolicyModeInvalid,
		},
		{
			name:    "empty allowlist entry is rejected",
			policy:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{""}},
			wantErr: ErrEgressPolicyAllowlistEntryEmpty,
		},
		{
			name:    "allowlist entry with a scheme is rejected",
			policy:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"https://example.com"}},
			wantErr: ErrEgressPolicyAllowlistEntryShape,
		},
		{
			name:    "allowlist entry with a path is rejected",
			policy:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"example.com/path"}},
			wantErr: ErrEgressPolicyAllowlistEntryShape,
		},
		{
			name:    "allowlist entry with whitespace is rejected",
			policy:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"example .com"}},
			wantErr: ErrEgressPolicyAllowlistEntryShape,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEgressPolicy(tt.policy)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateEgressPolicy(%+v) = %v, want nil", tt.policy, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateEgressPolicy(%+v) = %v, want error wrapping %v", tt.policy, err, tt.wantErr)
			}
		})
	}
}

func TestEgressPolicy_RequiresEnforcement(t *testing.T) {
	tests := []struct {
		name string
		mode EgressMode
		want bool
	}{
		{name: "unconfigured", mode: "", want: false},
		{name: "open", mode: EgressModeOpen, want: false},
		{name: "allowlist", mode: EgressModeAllowlist, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EgressPolicy{Mode: tt.mode}.RequiresEnforcement()
			if got != tt.want {
				t.Fatalf("EgressPolicy{Mode: %q}.RequiresEnforcement() = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestAppendAllowlistFloor(t *testing.T) {
	tests := []struct {
		name  string
		p     EgressPolicy
		floor []string
		want  EgressPolicy
	}{
		{
			name:  "open mode is a pure no-op regardless of floor",
			p:     EgressPolicy{Mode: EgressModeOpen},
			floor: []string{"cp.narvi.dev"},
			want:  EgressPolicy{Mode: EgressModeOpen},
		},
		{
			name:  "unconfigured mode is a pure no-op",
			p:     EgressPolicy{},
			floor: []string{"cp.narvi.dev"},
			want:  EgressPolicy{},
		},
		{
			name:  "empty customer allowlist gets exactly the floor",
			p:     EgressPolicy{Mode: EgressModeAllowlist},
			floor: []string{"cp.narvi.dev", "github.com"},
			want:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"cp.narvi.dev", "github.com"}},
		},
		{
			name:  "customer allowlist is merged with the floor, deduped, sorted",
			p:     EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"registry.npmjs.org", "github.com"}},
			floor: []string{"cp.narvi.dev", "github.com"},
			want:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"cp.narvi.dev", "github.com", "registry.npmjs.org"}},
		},
		{
			// The floor's own casing wins on a case-insensitive collision
			// (floor entries are appended first) -- the customer's
			// differently-cased duplicate is dropped, not kept alongside it.
			name:  "dedup is case-insensitive, floor casing wins",
			p:     EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"GitHub.com"}},
			floor: []string{"github.com"},
			want:  EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"github.com"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Snapshot the input allowlist's backing array before the call
			// so we can assert AppendAllowlistFloor never mutates it in
			// place -- the customer's own persisted row must stay exactly
			// as it was.
			original := append([]string(nil), tt.p.Allowlist...)

			got := AppendAllowlistFloor(tt.p, tt.floor)

			if got.Mode != tt.want.Mode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.want.Mode)
			}
			gotSorted := append([]string(nil), got.Allowlist...)
			if !reflect.DeepEqual(gotSorted, tt.want.Allowlist) && !(len(gotSorted) == 0 && len(tt.want.Allowlist) == 0) {
				t.Fatalf("Allowlist = %#v, want %#v", gotSorted, tt.want.Allowlist)
			}
			if !reflect.DeepEqual(tt.p.Allowlist, original) {
				t.Fatalf("AppendAllowlistFloor mutated its input p.Allowlist in place: got %#v, want unchanged %#v", tt.p.Allowlist, original)
			}
		})
	}
}

// TestAppendAllowlistFloor_FloorAlwaysPresent is the mutation-testable
// proof that the floor is genuinely appended, not merely validated: given
// a customer allowlist that omits BOTH the CP host and a session's own
// git host, the result must contain both anyway (Step 74 brief, point B).
func TestAppendAllowlistFloor_FloorAlwaysPresent(t *testing.T) {
	customerAllowlist := EgressPolicy{Mode: EgressModeAllowlist, Allowlist: []string{"registry.npmjs.org"}}
	cpHost := "cp.example.com"
	gitHost := "github.com"

	got := AppendAllowlistFloor(customerAllowlist, []string{cpHost, gitHost})

	foundCP, foundGit := false, false
	for _, h := range got.Allowlist {
		if h == cpHost {
			foundCP = true
		}
		if h == gitHost {
			foundGit = true
		}
	}
	if !foundCP {
		t.Fatalf("AppendAllowlistFloor result %#v does not contain the CP host %q", got.Allowlist, cpHost)
	}
	if !foundGit {
		t.Fatalf("AppendAllowlistFloor result %#v does not contain the session git host %q", got.Allowlist, gitHost)
	}
}

func TestHostFromURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
		wantOK bool
	}{
		{name: "https url with host", rawURL: "https://github.com/owner/repo.git", want: "github.com", wantOK: true},
		{name: "https url with port", rawURL: "https://gitlab.example.com:8443/owner/repo.git", want: "gitlab.example.com:8443", wantOK: true},
		{name: "unparseable url", rawURL: "://not a url", want: "", wantOK: false},
		{name: "no host", rawURL: "/just/a/path", want: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := HostFromURL(tt.rawURL)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("HostFromURL(%q) = (%q, %v), want (%q, %v)", tt.rawURL, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
