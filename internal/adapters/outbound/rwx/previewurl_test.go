package rwx_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
)

func TestFriendlyPreviewURL(t *testing.T) {
	tests := []struct {
		name             string
		endpointTemplate string
		prNumber         int
		orgSlug          string
		want             string
	}{
		{
			name:             "template with a {pr} placeholder",
			endpointTemplate: "myapp-pr-{pr}",
			prNumber:         42,
			orgSlug:          "acme",
			want:             "https://myapp-pr-42--acme.rwx.run",
		},
		{
			name:             "template with no placeholder is used verbatim",
			endpointTemplate: "docs-site",
			prNumber:         7,
			orgSlug:          "acme",
			want:             "https://docs-site--acme.rwx.run",
		},
		{
			name:             "placeholder appears more than once",
			endpointTemplate: "{pr}-{pr}",
			prNumber:         3,
			orgSlug:          "acme",
			want:             "https://3-3--acme.rwx.run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rwx.FriendlyPreviewURL(tt.endpointTemplate, tt.prNumber, tt.orgSlug)
			if got != tt.want {
				t.Errorf("FriendlyPreviewURL(%q, %d, %q) = %q, want %q",
					tt.endpointTemplate, tt.prNumber, tt.orgSlug, got, tt.want)
			}
		})
	}
}
