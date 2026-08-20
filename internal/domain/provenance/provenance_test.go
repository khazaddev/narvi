package provenance_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/provenance"
)

func strPtr(s string) *string { return &s }

func TestIsSentinelAutoFix_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		tag  *string
		want bool
	}{
		{"nil tag is not sentinel auto-fix", nil, false},
		{"exact match is sentinel auto-fix", strPtr(provenance.SentinelAutoFix), true},
		{"a different tag is not sentinel auto-fix", strPtr("scoped_environment"), false},
		{"empty string is not sentinel auto-fix", strPtr(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provenance.IsSentinelAutoFix(tt.tag); got != tt.want {
				t.Errorf("IsSentinelAutoFix(%v) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

// TestIsScopedEnvironment_TableDriven pins §14.4's own central gate
// (handoffsentinel.go's "an ordinary PR is completely untouched" check):
// only an EXACT "scoped_environment" tag counts, never a nil tag, a
// different known tag (SentinelAutoFix), or an empty string.
func TestIsScopedEnvironment_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		tag  *string
		want bool
	}{
		{"nil tag is not scoped", nil, false},
		{"exact match is scoped", strPtr(provenance.ScopedEnvironment), true},
		{"sentinel-auto-fix tag is not scoped", strPtr(provenance.SentinelAutoFix), false},
		{"empty string is not scoped", strPtr(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provenance.IsScopedEnvironment(tt.tag); got != tt.want {
				t.Errorf("IsScopedEnvironment(%v) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}
