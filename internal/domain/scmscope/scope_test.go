package scmscope_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/scmscope"
)

func TestValidateReadOnly(t *testing.T) {
	tests := []struct {
		name        string
		permissions map[string]string
		wantErr     bool
		wantNotRO   bool
		wantEmpty   bool
	}{
		{
			name:        "nil map is refused as empty",
			permissions: nil,
			wantErr:     true,
			wantEmpty:   true,
		},
		{
			name:        "empty map is refused as empty",
			permissions: map[string]string{},
			wantErr:     true,
			wantEmpty:   true,
		},
		{
			name:        "exactly contents+metadata read succeeds",
			permissions: map[string]string{"contents": "read", "metadata": "read"},
			wantErr:     false,
		},
		{
			name:        "a single read permission succeeds",
			permissions: map[string]string{"metadata": "read"},
			wantErr:     false,
		},
		{
			name:        "contents write is refused",
			permissions: map[string]string{"contents": "write", "metadata": "read"},
			wantErr:     true,
			wantNotRO:   true,
		},
		{
			name:        "admin on an unanticipated permission is refused",
			permissions: map[string]string{"contents": "read", "administration": "admin"},
			wantErr:     true,
			wantNotRO:   true,
		},
		{
			name:        "an unrecognized level string is refused",
			permissions: map[string]string{"contents": "READ"},
			wantErr:     true,
			wantNotRO:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := scmscope.ValidateReadOnly(tt.permissions)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateReadOnly(%v) = nil, want an error", tt.permissions)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateReadOnly(%v) = %v, want nil", tt.permissions, err)
			}
			if tt.wantEmpty {
				var emptyErr *scmscope.EmptyPermissionsError
				if !errors.As(err, &emptyErr) {
					t.Fatalf("ValidateReadOnly(%v) error = %v, want *scmscope.EmptyPermissionsError", tt.permissions, err)
				}
			}
			if tt.wantNotRO {
				var roErr *scmscope.NotReadOnlyError
				if !errors.As(err, &roErr) {
					t.Fatalf("ValidateReadOnly(%v) error = %v, want *scmscope.NotReadOnlyError", tt.permissions, err)
				}
			}
		})
	}
}
