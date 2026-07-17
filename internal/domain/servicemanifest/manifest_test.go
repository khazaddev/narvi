package servicemanifest_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/servicemanifest"
)

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func TestValidate_ValidManifest(t *testing.T) {
	t.Parallel()

	raw := servicemanifest.RawManifest{
		Services: []servicemanifest.RawService{
			{
				Name:        "web",
				Cmd:         "pnpm dev",
				Cwd:         "apps/web",
				Readiness:   servicemanifest.RawReadiness{Port: intPtr(3000)},
				Criticality: "primary",
			},
			{
				Name:        "mock-api",
				Cmd:         "prism mock contracts/api/openapi.yaml -p 4000",
				Readiness:   servicemanifest.RawReadiness{Health: strPtr("http://127.0.0.1:4000/health")},
				Criticality: "secondary",
			},
		},
	}

	got, err := servicemanifest.Validate(raw)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	if len(got.Services) != 2 {
		t.Fatalf("len(Services) = %d, want 2", len(got.Services))
	}

	web := got.Services[0]
	if web.Name != "web" || web.Cmd != "pnpm dev" || web.Cwd != "apps/web" {
		t.Errorf("web service = %+v, unexpected fields", web)
	}
	if web.Criticality != servicemanifest.CriticalityPrimary {
		t.Errorf("web.Criticality = %q, want %q", web.Criticality, servicemanifest.CriticalityPrimary)
	}
	if web.Readiness.Port == nil || *web.Readiness.Port != 3000 {
		t.Errorf("web.Readiness.Port = %v, want 3000", web.Readiness.Port)
	}
	if web.Readiness.Health != nil {
		t.Errorf("web.Readiness.Health = %v, want nil", web.Readiness.Health)
	}

	mock := got.Services[1]
	if mock.Criticality != servicemanifest.CriticalitySecondary {
		t.Errorf("mock.Criticality = %q, want %q", mock.Criticality, servicemanifest.CriticalitySecondary)
	}
	if mock.Readiness.Health == nil || *mock.Readiness.Health != "http://127.0.0.1:4000/health" {
		t.Errorf("mock.Readiness.Health = %v, want http://127.0.0.1:4000/health", mock.Readiness.Health)
	}
	if mock.Readiness.Port != nil {
		t.Errorf("mock.Readiness.Port = %v, want nil", mock.Readiness.Port)
	}
	if mock.Cwd != "" {
		t.Errorf("mock.Cwd = %q, want empty (repo root)", mock.Cwd)
	}
}

func TestValidate_InvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     servicemanifest.RawManifest
		wantErr any // a pointer to the expected error type, checked via errors.As
	}{
		{
			name:    "empty services list",
			raw:     servicemanifest.RawManifest{Services: []servicemanifest.RawService{}},
			wantErr: &servicemanifest.EmptyServicesError{},
		},
		{
			name:    "services entirely absent (zero value)",
			raw:     servicemanifest.RawManifest{},
			wantErr: &servicemanifest.EmptyServicesError{},
		},
		{
			name: "duplicate names",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "primary",
				},
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3001)}, Criticality: "secondary",
				},
			}},
			wantErr: &servicemanifest.DuplicateServiceNameError{},
		},
		{
			name: "blank name",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.MissingFieldError{},
		},
		{
			name: "blank cmd",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.MissingFieldError{},
		},
		{
			name: "invalid criticality",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "Primary",
				},
			}},
			wantErr: &servicemanifest.InvalidCriticalityError{},
		},
		{
			name: "invalid criticality no default",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "",
				},
			}},
			wantErr: &servicemanifest.InvalidCriticalityError{},
		},
		{
			name: "readiness with neither port nor health",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidReadinessError{},
		},
		{
			name: "readiness with both port and health",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{
						Port: intPtr(3000), Health: strPtr("http://127.0.0.1:3000/health"),
					},
					Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidReadinessError{},
		},
		{
			name: "out-of-range port (zero)",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(0)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidPortError{},
		},
		{
			name: "out-of-range port (too large)",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(70000)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidPortError{},
		},
		{
			name: "malformed health URL (no scheme/host)",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev",
					Readiness: servicemanifest.RawReadiness{Health: strPtr("not-a-url")}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidHealthURLError{},
		},
		{
			name: "cwd contains a .. segment",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev", Cwd: "../etc",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidCwdError{},
		},
		{
			name: "cwd is absolute",
			raw: servicemanifest.RawManifest{Services: []servicemanifest.RawService{
				{
					Name: "web", Cmd: "pnpm dev", Cwd: "/etc",
					Readiness: servicemanifest.RawReadiness{Port: intPtr(3000)}, Criticality: "primary",
				},
			}},
			wantErr: &servicemanifest.InvalidCwdError{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := servicemanifest.Validate(tc.raw)
			if err == nil {
				t.Fatalf("Validate() error = nil, want an error")
			}

			switch want := tc.wantErr.(type) {
			case *servicemanifest.EmptyServicesError:
				var got *servicemanifest.EmptyServicesError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *EmptyServicesError", err)
				}
			case *servicemanifest.DuplicateServiceNameError:
				var got *servicemanifest.DuplicateServiceNameError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *DuplicateServiceNameError", err)
				}
			case *servicemanifest.MissingFieldError:
				var got *servicemanifest.MissingFieldError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *MissingFieldError", err)
				}
			case *servicemanifest.InvalidCriticalityError:
				var got *servicemanifest.InvalidCriticalityError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *InvalidCriticalityError", err)
				}
			case *servicemanifest.InvalidReadinessError:
				var got *servicemanifest.InvalidReadinessError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *InvalidReadinessError", err)
				}
			case *servicemanifest.InvalidPortError:
				var got *servicemanifest.InvalidPortError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *InvalidPortError", err)
				}
			case *servicemanifest.InvalidHealthURLError:
				var got *servicemanifest.InvalidHealthURLError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *InvalidHealthURLError", err)
				}
			case *servicemanifest.InvalidCwdError:
				var got *servicemanifest.InvalidCwdError
				if !errors.As(err, &got) {
					t.Fatalf("Validate() error = %v, want *InvalidCwdError", err)
				}
			default:
				t.Fatalf("unhandled wantErr type %T", want)
			}
		})
	}
}
