package capabilityimportban

import "testing"

// TestSkipFile_DecidesOnPackagePathNotCheckoutLocation pins the property
// the allow-list exists to have: whether a file may import the registry is
// a fact about the code, never about where the repository sits on disk.
//
// This test exists because the allow-list was once a substring match over
// the absolute filename, with single-segment entries "/extension/" and
// "/controlplane/". Any clone under a directory of either name -- and any
// sub-package named either -- exempted the file, so the whole ban went
// silently off and the analyzer's own suite failed with "no diagnostic"
// for its planted violations. The rows below are those two reproductions,
// kept as tests so the substring form cannot come back unnoticed.
func TestSkipFile_DecidesOnPackagePathNotCheckoutLocation(t *testing.T) {
	t.Parallel()

	const (
		shadow  = "github.com/narvidev/narvi/internal/app/shadowscm"
		httpapi = "github.com/narvidev/narvi/internal/adapters/inbound/httpapi"
	)

	tests := []struct {
		name     string
		pkgPath  string
		filename string
		want     bool
	}{
		{
			name:     "suppression package in a checkout under a directory named extension",
			pkgPath:  shadow,
			filename: "/tmp/extension/narvi/internal/app/shadowscm/synthetic.go",
			want:     false,
		},
		{
			name:     "suppression package in a checkout under a directory named controlplane",
			pkgPath:  shadow,
			filename: "/home/ci/controlplane/narvi/internal/app/shadowscm/synthetic.go",
			want:     false,
		},
		{
			name:     "sub-package literally named extension inside a suppression package",
			pkgPath:  shadow + "/extension",
			filename: "/repo/internal/app/shadowscm/extension/gate.go",
			want:     false,
		},
		{
			name:     "the real composition root, wherever it is checked out",
			pkgPath:  "github.com/narvidev/narvi/controlplane",
			filename: "/somewhere/odd/controlplane/serve.go",
			want:     true,
		},
		{
			name:     "the real facade",
			pkgPath:  "github.com/narvidev/narvi/extension",
			filename: "/somewhere/odd/extension/module.go",
			want:     true,
		},
		{
			name:     "an allowed httpapi file",
			pkgPath:  httpapi,
			filename: "/repo/internal/adapters/inbound/httpapi/requirecapability.go",
			want:     true,
		},
		{
			name:     "another file in the same allowed package is NOT allowed",
			pkgPath:  httpapi,
			filename: "/repo/internal/adapters/inbound/httpapi/scmcredentials.go",
			want:     false,
		},
		{
			name:     "an allowed base name in the WRONG package is not allowed",
			pkgPath:  shadow,
			filename: "/repo/internal/app/shadowscm/requirecapability.go",
			want:     false,
		},
		{
			name:     "tests are exempt wherever they live",
			pkgPath:  shadow,
			filename: "/repo/internal/app/shadowscm/synthetic_test.go",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := skipFile(tt.pkgPath, tt.filename); got != tt.want {
				t.Errorf("skipFile(%q, %q) = %v, want %v", tt.pkgPath, tt.filename, got, tt.want)
			}
		})
	}
}
