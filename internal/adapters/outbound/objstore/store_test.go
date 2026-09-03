package objstore

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// testConfig builds a valid Config pointed at endpoint, mirroring
// modal.testConfig's own shape exactly (internal/adapters/outbound/modal/
// provider_test.go).
func testConfig(endpoint string) Config {
	timeouts := platform.DefaultTimeouts()
	timeouts.ObjectStoreHTTPClientTimeout = 5 * time.Second
	return Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		Bucket:          "test-bucket",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		UsePathStyle:    true,
		Timeouts:        timeouts,
	}
}

// closedPort returns the address of a TCP port that was briefly listened
// on and immediately closed -- connecting to it fails fast with
// "connection refused" rather than hanging, mirroring modal's own
// TestProvider_NetworkErrorIsTransient precedent exactly. Used as a decoy
// "this must never actually be dialed" endpoint.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}
	return "http://" + addr
}

// --- New() validation ---

func TestNew_Validation(t *testing.T) {
	valid := testConfig("http://127.0.0.1:9")

	t.Run("missing endpoint", func(t *testing.T) {
		cfg := valid
		cfg.Endpoint = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Fatalf("New() error = %v, want *MissingConfigError", err)
		}
		if target.Field != "Endpoint" {
			t.Errorf("Field = %q, want %q", target.Field, "Endpoint")
		}
	})

	t.Run("missing region", func(t *testing.T) {
		cfg := valid
		cfg.Region = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Fatalf("New() error = %v, want *MissingConfigError", err)
		}
		if target.Field != "Region" {
			t.Errorf("Field = %q, want %q", target.Field, "Region")
		}
	})

	t.Run("missing bucket", func(t *testing.T) {
		cfg := valid
		cfg.Bucket = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Fatalf("New() error = %v, want *MissingConfigError", err)
		}
		if target.Field != "Bucket" {
			t.Errorf("Field = %q, want %q", target.Field, "Bucket")
		}
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		s, err := New(valid)
		if err != nil {
			t.Fatalf("New() error = %v, want nil", err)
		}
		if s == nil {
			t.Fatal("New() returned nil Store with nil error")
		}
	})

	t.Run("empty static credentials fall back to the default chain without erroring", func(t *testing.T) {
		cfg := valid
		cfg.AccessKeyID = ""
		cfg.SecretAccessKey = ""
		_, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v, want nil (default credential chain path, §28.7)", err)
		}
	})
}

// --- Stat/Delete: request shape (bucket/key/method/path) ---

func TestStatDelete_RequestShape(t *testing.T) {
	tests := []struct {
		name       string
		method     func(s *Store) error
		wantMethod string
	}{
		{
			name: "Stat issues HEAD",
			method: func(s *Store) error {
				_, err := s.Stat(context.Background(), ports.BlobKey("sessions/abc/uploads/def"))
				return err
			},
			wantMethod: http.MethodHead,
		},
		{
			name: "Delete issues DELETE",
			method: func(s *Store) error {
				return s.Delete(context.Background(), ports.BlobKey("sessions/abc/uploads/def"))
			},
			wantMethod: http.MethodDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			s, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if err := tt.method(s); err != nil {
				t.Fatalf("method call error = %v", err)
			}

			if gotMethod != tt.wantMethod {
				t.Errorf("request method = %q, want %q", gotMethod, tt.wantMethod)
			}
			// UsePathStyle: true -> path-style addressing, bucket in the URL path.
			wantPath := "/test-bucket/sessions/abc/uploads/def"
			if gotPath != wantPath {
				t.Errorf("request path = %q, want %q", gotPath, wantPath)
			}
		})
	}
}

// TestStatDelete_NeverUsePublicEndpoint proves Stat/Delete always call
// Config.Endpoint, never Config.PublicEndpoint (§28.7: "Stat/Delete never
// use this field; they always call Endpoint directly") -- PublicEndpoint
// is pointed at a closed port that fails fast if ever dialed, so a
// successful Stat/Delete against the real Endpoint server proves the
// separation rather than merely assuming it.
func TestStatDelete_NeverUsePublicEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.PublicEndpoint = closedPort(t)

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := s.Stat(context.Background(), ports.BlobKey("k")); err != nil {
		t.Errorf("Stat() error = %v, want nil (must hit Endpoint, not the closed-port PublicEndpoint)", err)
	}
	if err := s.Delete(context.Background(), ports.BlobKey("k")); err != nil {
		t.Errorf("Delete() error = %v, want nil (must hit Endpoint, not the closed-port PublicEndpoint)", err)
	}
}

// --- Stat: success shape (SizeBytes/ETag, including quote-trimming) ---

func TestStat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.Header().Set("Content-Length", "1234")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	info, err := s.Stat(context.Background(), ports.BlobKey("k"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.SizeBytes != 1234 {
		t.Errorf("SizeBytes = %d, want 1234", info.SizeBytes)
	}
	if info.ETag != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("ETag = %q, want unquoted %q", info.ETag, "d41d8cd98f00b204e9800998ecf8427e")
	}
}

// --- Stat/Delete: HTTP-status classification table ---
//
// Covers, at minimum, the exit-bar statuses named for this adapter: 400,
// 403, 404 (Stat/Delete differ -- see below), 409, 413 (oversize, §28.7's
// own exit bar), 429, 500, 503, and one unrecognized code.

func TestStatDelete_Classification(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantTransient bool // meaningless for 404 -- both methods special-case it below
	}{
		{"400 bad request", http.StatusBadRequest, false},
		{"403 forbidden", http.StatusForbidden, false},
		{"404 not found", http.StatusNotFound, false},
		{"409 conflict", http.StatusConflict, false},
		{"413 request entity too large", http.StatusRequestEntityTooLarge, false},
		{"429 too many requests", http.StatusTooManyRequests, true},
		{"500 internal server error", http.StatusInternalServerError, true},
		{"503 service unavailable", http.StatusServiceUnavailable, true},
		{"418 unrecognized code defaults to transient", http.StatusTeapot, true},
	}

	for _, tt := range tests {
		t.Run("Stat/"+tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			s, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			_, err = s.Stat(context.Background(), ports.BlobKey("k"))
			if err == nil {
				t.Fatal("Stat() error = nil, want an error")
			}

			if tt.status == http.StatusNotFound {
				if !errors.Is(err, ports.ErrBlobNotFound) {
					t.Errorf("Stat() error = %v, want errors.Is(err, ports.ErrBlobNotFound)", err)
				}
				var be *ports.BlobStoreError
				if errors.As(err, &be) {
					t.Errorf("Stat() error = %v, must NOT also match *ports.BlobStoreError (§28.1: not-found is distinct)", err)
				}
				return
			}

			var be *ports.BlobStoreError
			if !errors.As(err, &be) {
				t.Fatalf("Stat() error = %v, want *ports.BlobStoreError", err)
			}
			if be.Transient != tt.wantTransient {
				t.Errorf("status %d: Transient = %v, want %v", tt.status, be.Transient, tt.wantTransient)
			}
			if be.Op != ports.BlobOpStat {
				t.Errorf("status %d: Op = %q, want %q", tt.status, be.Op, ports.BlobOpStat)
			}
			if got := ports.IsBlobStoreTransient(err); got != tt.wantTransient {
				t.Errorf("status %d: ports.IsBlobStoreTransient = %v, want %v", tt.status, got, tt.wantTransient)
			}
		})

		t.Run("Delete/"+tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			s, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			err = s.Delete(context.Background(), ports.BlobKey("k"))

			if tt.status == http.StatusNotFound {
				// Delete's own idempotency contract: an already-absent key
				// is success, never ports.ErrBlobNotFound (that sentinel is
				// Stat-only).
				if err != nil {
					t.Errorf("Delete() error = %v, want nil (idempotent on an already-absent key)", err)
				}
				return
			}

			if err == nil {
				t.Fatal("Delete() error = nil, want an error")
			}
			var be *ports.BlobStoreError
			if !errors.As(err, &be) {
				t.Fatalf("Delete() error = %v, want *ports.BlobStoreError", err)
			}
			if be.Transient != tt.wantTransient {
				t.Errorf("status %d: Transient = %v, want %v", tt.status, be.Transient, tt.wantTransient)
			}
			if be.Op != ports.BlobOpDelete {
				t.Errorf("status %d: Op = %q, want %q", tt.status, be.Op, ports.BlobOpDelete)
			}
		})
	}
}

// --- Never embed the raw response body ---

func TestStatDelete_NeverEmbedRawBody(t *testing.T) {
	const secret = "super-secret-value-should-never-leak-into-a-log-line"

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "malformed (non-XML) body with an embedded secret",
			status: http.StatusForbidden,
			body:   "not xml, but contains " + secret,
		},
		{
			name:   "xml-shaped body with a secret in a field this adapter never reads",
			status: http.StatusInternalServerError,
			body:   `<?xml version="1.0"?><Error><Code>InternalError</Code><UnmodeledField>` + secret + `</UnmodeledField></Error>`,
		},
	}

	for _, tt := range tests {
		t.Run("Stat/"+tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			s, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = s.Stat(context.Background(), ports.BlobKey("k"))
			if err == nil {
				t.Fatal("Stat() error = nil, want an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Stat() error = %q, must never contain the raw response body/secret", err.Error())
			}
		})

		t.Run("Delete/"+tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			s, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			err = s.Delete(context.Background(), ports.BlobKey("k"))
			if err == nil {
				t.Fatal("Delete() error = nil, want an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Delete() error = %q, must never contain the raw response body/secret", err.Error())
			}
		})
	}
}

// --- Network-level failures ---

func TestStatDelete_NetworkErrorIsTransient(t *testing.T) {
	addr := closedPort(t)

	s, err := New(testConfig(addr))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Run("Stat", func(t *testing.T) {
		_, err := s.Stat(context.Background(), ports.BlobKey("k"))
		if err == nil {
			t.Fatal("Stat() error = nil, want a network-level error")
		}
		var be *ports.BlobStoreError
		if !errors.As(err, &be) {
			t.Fatalf("Stat() error = %v, want *ports.BlobStoreError", err)
		}
		if !be.Transient {
			t.Error("connection-refused error: Transient = false, want true")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		err := s.Delete(context.Background(), ports.BlobKey("k"))
		if err == nil {
			t.Fatal("Delete() error = nil, want a network-level error")
		}
		var be *ports.BlobStoreError
		if !errors.As(err, &be) {
			t.Fatalf("Delete() error = %v, want *ports.BlobStoreError", err)
		}
		if !be.Transient {
			t.Error("connection-refused error: Transient = false, want true")
		}
	})
}

// --- Timeout wiring ---

func TestStatDelete_BoundedByObjectStoreHTTPClientTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block // never respond within the test's short client timeout
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	cfg := testConfig(srv.URL)
	cfg.Timeouts.ObjectStoreHTTPClientTimeout = 50 * time.Millisecond

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Run("Stat", func(t *testing.T) {
		start := time.Now()
		_, err := s.Stat(context.Background(), ports.BlobKey("k"))
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Stat() error = nil, want a timeout error")
		}
		if elapsed > 5*time.Second {
			t.Errorf("Stat() took %s, want it bounded near the 50ms ObjectStoreHTTPClientTimeout", elapsed)
		}
		var be *ports.BlobStoreError
		if !errors.As(err, &be) {
			t.Fatalf("Stat() error = %v, want *ports.BlobStoreError", err)
		}
		if !be.Transient {
			t.Error("Stat() timeout error Transient = false, want true")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		start := time.Now()
		err := s.Delete(context.Background(), ports.BlobKey("k"))
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Delete() error = nil, want a timeout error")
		}
		if elapsed > 5*time.Second {
			t.Errorf("Delete() took %s, want it bounded near the 50ms ObjectStoreHTTPClientTimeout", elapsed)
		}
		var be *ports.BlobStoreError
		if !errors.As(err, &be) {
			t.Fatalf("Delete() error = %v, want *ports.BlobStoreError", err)
		}
		if !be.Transient {
			t.Error("Delete() timeout error Transient = false, want true")
		}
	})
}
