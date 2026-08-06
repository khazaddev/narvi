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
			got, err := rwx.FriendlyPreviewURL(tt.endpointTemplate, tt.prNumber, tt.orgSlug)
			if err != nil {
				t.Fatalf("FriendlyPreviewURL(%q, %d, %q) unexpected error = %v",
					tt.endpointTemplate, tt.prNumber, tt.orgSlug, err)
			}
			if got != tt.want {
				t.Errorf("FriendlyPreviewURL(%q, %d, %q) = %q, want %q",
					tt.endpointTemplate, tt.prNumber, tt.orgSlug, got, tt.want)
			}
		})
	}
}

// TestFriendlyPreviewURL_HostPinValidation is the security-fix regression
// test (S2, "host-pin the rendered preview URL"): FriendlyPreviewURL used
// to string-concatenate endpointTemplate/orgSlug into "https://..." with no
// validation at all, so a hostile or merely corrupted template could
// render a URL entirely off the rwx.run domain that the platform bot would
// then vouch for via the narvi/preview commit status. Each rejected case
// below isolates exactly ONE of the validation's checks (scheme, userinfo,
// host suffix, non-empty inputs) rather than combining several into one
// compound example, so a regression in any single check fails its own
// dedicated case.
func TestFriendlyPreviewURL_HostPinValidation(t *testing.T) {
	tests := []struct {
		name             string
		endpointTemplate string
		prNumber         int
		orgSlug          string
		wantURL          string // only checked when wantErr is false
		wantErr          bool
	}{
		{
			name:             "legit template passes",
			endpointTemplate: "myapp-pr-{pr}",
			prNumber:         42,
			orgSlug:          "acme",
			wantURL:          "https://myapp-pr-42--acme.rwx.run",
		},
		{
			// "evil@" renders "https://evil@--acme.rwx.run": host alone
			// ("--acme.rwx.run") would otherwise satisfy the suffix check,
			// isolating that this case is rejected specifically because of
			// the userinfo ("evil@"), not an incidentally-wrong host --
			// url.Parse quietly treats "evil" as authentication info for
			// whatever host follows, discarding it, rather than as part of
			// the trusted host itself.
			name:             `"@"-userinfo trick is rejected`,
			endpointTemplate: "evil@",
			orgSlug:          "acme",
			wantErr:          true,
		},
		{
			// The trailing "/" ends the authority component before
			// ".rwx.run" is ever reached, so url.Parse resolves the actual
			// host to "evil.example.com" and pushes "--acme.rwx.run" into
			// the path instead -- exactly the "renders an off-domain URL"
			// risk this fix's own doc comment names.
			name:             "off-domain host is rejected",
			endpointTemplate: "evil.example.com/",
			orgSlug:          "acme",
			wantErr:          true,
		},
		{
			// An attempt to smuggle a second scheme+host via the template.
			// The hardcoded "https://" prefix means the SCHEME can never
			// actually be overridden this way, but url.Parse resolves the
			// authority to host "http" alone (everything from the injected
			// "://" onward becomes path) -- still correctly rejected, via
			// the host-suffix check, proving the injection gains nothing.
			name:             "scheme-injection-shaped template is rejected",
			endpointTemplate: "http://evil.com",
			orgSlug:          "acme",
			wantErr:          true,
		},
		{
			name:             "empty template is rejected",
			endpointTemplate: "",
			orgSlug:          "acme",
			wantErr:          true,
		},
		{
			name:             "empty org slug is rejected",
			endpointTemplate: "docs-site",
			orgSlug:          "",
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rwx.FriendlyPreviewURL(tt.endpointTemplate, tt.prNumber, tt.orgSlug)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FriendlyPreviewURL(%q, %d, %q) error = nil, want a host-pin validation error; got url %q",
						tt.endpointTemplate, tt.prNumber, tt.orgSlug, got)
				}
				if got != "" {
					t.Errorf("FriendlyPreviewURL(%q, %d, %q) = %q, want an empty string alongside a non-nil error",
						tt.endpointTemplate, tt.prNumber, tt.orgSlug, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("FriendlyPreviewURL(%q, %d, %q) unexpected error = %v",
					tt.endpointTemplate, tt.prNumber, tt.orgSlug, err)
			}
			if got != tt.wantURL {
				t.Errorf("FriendlyPreviewURL(%q, %d, %q) = %q, want %q",
					tt.endpointTemplate, tt.prNumber, tt.orgSlug, got, tt.wantURL)
			}
		})
	}
}
