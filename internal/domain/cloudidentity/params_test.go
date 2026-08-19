package cloudidentity_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
)

func TestValidateParams(t *testing.T) {
	tests := []struct {
		name    string
		kind    cloudidentity.Kind
		raw     string
		wantErr error
	}{
		{"aws roleArn ok", cloudidentity.KindAWS, `{"roleArn":"arn:aws:iam::123456789012:role/narvi"}`, nil},
		{"aws missing roleArn", cloudidentity.KindAWS, `{}`, cloudidentity.ErrRoleARNRequired},
		{"aws blank roleArn", cloudidentity.KindAWS, `{"roleArn":"  "}`, cloudidentity.ErrRoleARNRequired},
		{"aws malformed json", cloudidentity.KindAWS, `nope`, cloudidentity.ErrParamsMalformed},
		{"gcp workloadIdentityProvider ok", cloudidentity.KindGCP, `{"workloadIdentityProvider":"projects/123/locations/global/workloadIdentityPools/p/providers/pr"}`, nil},
		{"gcp with optional service account ok", cloudidentity.KindGCP, `{"workloadIdentityProvider":"wip","serviceAccountEmail":"sa@project.iam.gserviceaccount.com"}`, nil},
		{"gcp missing workloadIdentityProvider", cloudidentity.KindGCP, `{}`, cloudidentity.ErrWorkloadIdentityProviderRequired},
		{"azure clientId+tenantId ok", cloudidentity.KindAzure, `{"clientId":"c","tenantId":"t"}`, nil},
		{"azure missing clientId", cloudidentity.KindAzure, `{"tenantId":"t"}`, cloudidentity.ErrClientIDRequired},
		{"azure missing tenantId", cloudidentity.KindAzure, `{"clientId":"c"}`, cloudidentity.ErrTenantIDRequired},
		{"generic envVar ok", cloudidentity.KindGeneric, `{"envVar":"MY_TOKEN_PATH"}`, nil},
		{"generic missing envVar", cloudidentity.KindGeneric, `{}`, cloudidentity.ErrEnvVarRequired},
		{"unrecognized kind", cloudidentity.Kind("digitalocean"), `{}`, cloudidentity.ErrInvalidKind},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cloudidentity.ValidateParams(tc.kind, json.RawMessage(tc.raw))
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateParams(...) = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateParams(...) = %v, want error wrapping %v", err, tc.wantErr)
			}
		})
	}
}
