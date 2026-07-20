package servicemanifest_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/servicemanifest"
)

// TestErrorMethods exercises every Error() method this package's own
// sentinel/named error types define -- these are simple, mechanical
// string-formatting methods with no branching, so a table-driven test
// constructing each error value directly and asserting its Error() string
// is non-empty and contains every value the error carries is all that's
// warranted, matching TestValidate_InvalidInputs's own table-driven style
// above.
func TestErrorMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		wantAll []string // every substring Error() must contain
	}{
		{
			name:    "EmptyServicesError",
			err:     &servicemanifest.EmptyServicesError{},
			wantAll: []string{"services list is empty"},
		},
		{
			name: "MissingFieldError",
			err: &servicemanifest.MissingFieldError{
				Index: 2, Name: "web", Field: "cmd",
			},
			wantAll: []string{"2", "web", "cmd"},
		},
		{
			name:    "DuplicateServiceNameError",
			err:     &servicemanifest.DuplicateServiceNameError{Name: "web"},
			wantAll: []string{"web"},
		},
		{
			name: "InvalidCriticalityError",
			err: &servicemanifest.InvalidCriticalityError{
				Name: "web", Value: "Primary",
			},
			wantAll: []string{
				"web", "Primary",
				string(servicemanifest.CriticalityPrimary),
				string(servicemanifest.CriticalitySecondary),
			},
		},
		{
			name: "InvalidReadinessError",
			err: &servicemanifest.InvalidReadinessError{
				Name: "web", Reason: "neither port nor health set",
			},
			wantAll: []string{"web", "neither port nor health set"},
		},
		{
			name: "InvalidPortError",
			err: &servicemanifest.InvalidPortError{
				Name: "web", Port: 70000,
			},
			wantAll: []string{"web", "70000"},
		},
		{
			name: "InvalidHealthURLError",
			err: &servicemanifest.InvalidHealthURLError{
				Name: "web", Value: "not-a-url",
			},
			wantAll: []string{"web", "not-a-url"},
		},
		{
			name: "InvalidCwdError",
			err: &servicemanifest.InvalidCwdError{
				Name: "web", Cwd: "../etc", Reason: "must not escape the repo root",
			},
			wantAll: []string{"web", "../etc", "must not escape the repo root"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.err.Error()
			if got == "" {
				t.Fatal("Error() = \"\", want non-empty")
			}
			for _, want := range tc.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("Error() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}
