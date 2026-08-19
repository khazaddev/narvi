package oidckey_test

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/oidckey"
)

func TestIsActive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	retired := now.Add(-time.Minute)

	tests := []struct {
		name string
		k    oidckey.SigningKey
		want bool
	}{
		{"nil RetiredAt is active", oidckey.SigningKey{Kid: "k1", RetiredAt: nil}, true},
		{"non-nil RetiredAt is not active", oidckey.SigningKey{Kid: "k2", RetiredAt: &retired}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oidckey.IsActive(tc.k); got != tc.want {
				t.Errorf("IsActive(%+v) = %v, want %v", tc.k, got, tc.want)
			}
		})
	}
}

func TestIsPublishable(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	overlap := 15 * time.Minute

	justRetired := now.Add(-time.Minute)
	exactlyAtBoundary := now.Add(-overlap)
	pastBoundary := now.Add(-overlap - time.Second)

	tests := []struct {
		name string
		k    oidckey.SigningKey
		want bool
	}{
		{"active key (nil RetiredAt) is always publishable", oidckey.SigningKey{Kid: "active", RetiredAt: nil}, true},
		{"just-retired key is still publishable", oidckey.SigningKey{Kid: "recent", RetiredAt: &justRetired}, true},
		{"retired exactly overlap ago is no longer publishable (now.Before is strict)", oidckey.SigningKey{Kid: "boundary", RetiredAt: &exactlyAtBoundary}, false},
		{"retired more than overlap ago is not publishable", oidckey.SigningKey{Kid: "stale", RetiredAt: &pastBoundary}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oidckey.IsPublishable(tc.k, now, overlap); got != tc.want {
				t.Errorf("IsPublishable(%+v, %v, %v) = %v, want %v", tc.k, now, overlap, got, tc.want)
			}
		})
	}
}

func TestFilterPublishable(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	overlap := 15 * time.Minute
	justRetired := now.Add(-time.Minute)
	pastBoundary := now.Add(-overlap - time.Second)

	keys := []oidckey.SigningKey{
		{Kid: "active", RetiredAt: nil},
		{Kid: "recent", RetiredAt: &justRetired},
		{Kid: "stale", RetiredAt: &pastBoundary},
	}

	got := oidckey.FilterPublishable(keys, now, overlap)
	if len(got) != 2 {
		t.Fatalf("FilterPublishable returned %d keys, want 2: %+v", len(got), got)
	}
	if got[0].Kid != "active" || got[1].Kid != "recent" {
		t.Errorf("FilterPublishable = %+v, want [active, recent] in that order", got)
	}
}

func TestFilterPublishable_EmptyInput(t *testing.T) {
	now := time.Now()
	got := oidckey.FilterPublishable(nil, now, time.Minute)
	if len(got) != 0 {
		t.Errorf("FilterPublishable(nil, ...) = %+v, want empty", got)
	}
}
