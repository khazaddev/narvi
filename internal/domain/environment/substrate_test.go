package environment

import (
	"errors"
	"testing"
)

func TestCheckSubstrateCapabilities(t *testing.T) {
	tests := []struct {
		name                      string
		dockerRequired            bool
		egressEnforcementRequired bool
		providerSupportsDocker    bool
		providerSupportsEgress    bool
		wantErr                   error
	}{
		{name: "nothing required, unsupported provider is fine", providerSupportsDocker: false, providerSupportsEgress: false},
		{name: "nothing required, fully supported provider is fine", providerSupportsDocker: true, providerSupportsEgress: true},
		{
			name:                   "docker required and supported",
			dockerRequired:         true,
			providerSupportsDocker: true,
			wantErr:                nil,
		},
		{
			name:                   "docker required but not supported",
			dockerRequired:         true,
			providerSupportsDocker: false,
			wantErr:                ErrDockerUnsupported,
		},
		{
			name:                      "egress enforcement required and supported",
			egressEnforcementRequired: true,
			providerSupportsEgress:    true,
			wantErr:                   nil,
		},
		{
			name:                      "egress enforcement required but not supported",
			egressEnforcementRequired: true,
			providerSupportsEgress:    false,
			wantErr:                   ErrEgressEnforcementUnsupported,
		},
		{
			name:                      "both required, docker fails first (deterministic ordering)",
			dockerRequired:            true,
			egressEnforcementRequired: true,
			providerSupportsDocker:    false,
			providerSupportsEgress:    false,
			wantErr:                   ErrDockerUnsupported,
		},
		{
			name:                      "both required, docker supported but egress not",
			dockerRequired:            true,
			egressEnforcementRequired: true,
			providerSupportsDocker:    true,
			providerSupportsEgress:    false,
			wantErr:                   ErrEgressEnforcementUnsupported,
		},
		{
			name:                      "both required and both supported",
			dockerRequired:            true,
			egressEnforcementRequired: true,
			providerSupportsDocker:    true,
			providerSupportsEgress:    true,
			wantErr:                   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckSubstrateCapabilities(tt.dockerRequired, tt.egressEnforcementRequired, tt.providerSupportsDocker, tt.providerSupportsEgress)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("CheckSubstrateCapabilities(...) = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CheckSubstrateCapabilities(...) = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestCheckSubstrateCapabilities_EgressOpenNeverRequiresProviderSupport
// proves EgressPolicy.RequiresEnforcement()'s own contract holds through
// this function too: an "open" (or unconfigured) egress mode never
// refuses a spawn regardless of provider support, because callers are
// expected to pass egressEnforcementRequired = false for it (see
// EgressPolicy.RequiresEnforcement's own doc comment) -- this test pins
// that CheckSubstrateCapabilities itself does not second-guess that input.
func TestCheckSubstrateCapabilities_EgressOpenNeverRequiresProviderSupport(t *testing.T) {
	policy := EgressPolicy{Mode: EgressModeOpen}
	if policy.RequiresEnforcement() {
		t.Fatalf("EgressPolicy{Mode: EgressModeOpen}.RequiresEnforcement() = true, want false")
	}
	if err := CheckSubstrateCapabilities(false, policy.RequiresEnforcement(), true, false); err != nil {
		t.Fatalf("CheckSubstrateCapabilities with an open policy and no provider egress support = %v, want nil", err)
	}
}
