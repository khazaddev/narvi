package objstore

import (
	"context"
	"errors"
	"mime"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/app/ports"
)

// --- PresignPut ---

func TestPresignPut_SignsRequestedHeaders(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	presigned, err := s.PresignPut(context.Background(), ports.PresignPutSpec{
		Key:           ports.BlobKey("sessions/abc/uploads/def"),
		ContentType:   "image/png",
		ContentLength: 1234,
		TTL:           5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignPut() error = %v", err)
	}

	if presigned.URL == "" {
		t.Error("URL is empty")
	}
	if got := presigned.Headers["Content-Type"]; got != "image/png" {
		t.Errorf("Headers[Content-Type] = %q, want %q", got, "image/png")
	}
	if got := presigned.Headers["Content-Length"]; got != "1234" {
		t.Errorf("Headers[Content-Length] = %q, want %q", got, "1234")
	}
}

func TestPresignPut_OmitsContentTypeAndLengthWhenUnset(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	presigned, err := s.PresignPut(context.Background(), ports.PresignPutSpec{
		Key: ports.BlobKey("k"),
		TTL: 5 * time.Minute,
		// ContentType/ContentLength deliberately left zero-value.
	})
	if err != nil {
		t.Fatalf("PresignPut() error = %v", err)
	}
	if _, ok := presigned.Headers["Content-Type"]; ok {
		t.Errorf("Headers[Content-Type] present = %v, want absent when spec.ContentType is empty", presigned.Headers["Content-Type"])
	}
	if _, ok := presigned.Headers["Content-Length"]; ok {
		t.Errorf("Headers[Content-Length] present = %v, want absent when spec.ContentLength is zero", presigned.Headers["Content-Length"])
	}
}

func TestPresignPut_ExpiresAtApproximatesNowPlusTTL(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ttl := 10 * time.Minute
	before := time.Now().Add(ttl)
	got, err := s.PresignPut(context.Background(), ports.PresignPutSpec{Key: "k", TTL: ttl})
	after := time.Now().Add(ttl)
	if err != nil {
		t.Fatalf("PresignPut() error = %v", err)
	}
	if got.ExpiresAt.Before(before) || got.ExpiresAt.After(after) {
		t.Errorf("ExpiresAt = %v, want between %v and %v", got.ExpiresAt, before, after)
	}
}

func TestPresignPut_ErrorIsPermanent(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// An empty Key is a local, no-network serialization validation failure
	// (verified directly: "input member Key must not be empty") -- the
	// simplest deterministic way to force PresignPut to fail without any
	// real network call.
	_, err = s.PresignPut(context.Background(), ports.PresignPutSpec{Key: "", TTL: time.Minute})
	if err == nil {
		t.Fatal("PresignPut() error = nil, want an error for an empty Key")
	}
	var be *ports.BlobStoreError
	if !errors.As(err, &be) {
		t.Fatalf("PresignPut() error = %v, want *ports.BlobStoreError", err)
	}
	if be.Transient {
		t.Error("PresignPut() error.Transient = true, want false (§28.1: presign never fails transiently)")
	}
	if be.Op != ports.BlobOpPresignPut {
		t.Errorf("PresignPut() error.Op = %q, want %q", be.Op, ports.BlobOpPresignPut)
	}
	if ports.IsBlobStoreTransient(err) {
		t.Error("ports.IsBlobStoreTransient(err) = true, want false")
	}
}

// --- PresignGet ---

func TestPresignGet_NoResponseContentDispositionWhenFilenameEmpty(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := s.PresignGet(context.Background(), ports.PresignGetSpec{Key: "k", TTL: time.Minute})
	if err != nil {
		t.Fatalf("PresignGet() error = %v", err)
	}
	if strings.Contains(got.URL, "response-content-disposition") {
		t.Errorf("URL = %q, must not carry response-content-disposition when ResponseFilename is empty", got.URL)
	}
}

func TestPresignGet_ResponseContentDisposition_CorrectlyEscaped(t *testing.T) {
	// ResponseFilename is attacker-influenced (a user-supplied upload's own
	// filename) -- this exercises exactly the characters naive string
	// concatenation gets wrong: an embedded double quote and backslash.
	filenames := []string{
		`my file "quoted".png`,
		`back\slash.txt`,
		`héllo wörld.csv`,
		"plain.pdf",
	}

	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, filename := range filenames {
		t.Run(filename, func(t *testing.T) {
			got, err := s.PresignGet(context.Background(), ports.PresignGetSpec{
				Key:              "k",
				TTL:              time.Minute,
				ResponseFilename: filename,
			})
			if err != nil {
				t.Fatalf("PresignGet() error = %v", err)
			}

			parsed, err := url.Parse(got.URL)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", got.URL, err)
			}
			gotDisposition := parsed.Query().Get("response-content-disposition")
			wantDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
			if gotDisposition != wantDisposition {
				t.Errorf("response-content-disposition = %q, want %q (mime.FormatMediaType's own output)", gotDisposition, wantDisposition)
			}

			// Never naive concatenation: an internal quote/backslash must be
			// backslash-escaped by FormatMediaType, never passed through
			// raw in a way that would let it terminate the quoted-string
			// early.
			if strings.ContainsAny(filename, `"\`) {
				if !strings.Contains(gotDisposition, `\"`) && !strings.Contains(gotDisposition, `\\`) {
					t.Errorf("response-content-disposition = %q, want internal quote/backslash to be backslash-escaped", gotDisposition)
				}
			}
		})
	}
}

func TestPresignGet_ExpiresAtApproximatesNowPlusTTL(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ttl := 2 * time.Minute
	before := time.Now().Add(ttl)
	got, err := s.PresignGet(context.Background(), ports.PresignGetSpec{Key: "k", TTL: ttl})
	after := time.Now().Add(ttl)
	if err != nil {
		t.Fatalf("PresignGet() error = %v", err)
	}
	if got.ExpiresAt.Before(before) || got.ExpiresAt.After(after) {
		t.Errorf("ExpiresAt = %v, want between %v and %v", got.ExpiresAt, before, after)
	}
}

func TestPresignGet_ErrorIsPermanent(t *testing.T) {
	s, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = s.PresignGet(context.Background(), ports.PresignGetSpec{Key: "", TTL: time.Minute})
	if err == nil {
		t.Fatal("PresignGet() error = nil, want an error for an empty Key")
	}
	var be *ports.BlobStoreError
	if !errors.As(err, &be) {
		t.Fatalf("PresignGet() error = %v, want *ports.BlobStoreError", err)
	}
	if be.Transient {
		t.Error("PresignGet() error.Transient = true, want false (§28.1: presign never fails transiently)")
	}
	if be.Op != ports.BlobOpPresignGet {
		t.Errorf("PresignGet() error.Op = %q, want %q", be.Op, ports.BlobOpPresignGet)
	}
}

// --- PublicEndpoint host binding (§28.7: "presigning binds the host") ---

func TestPresignPutGet_SignAgainstPublicEndpointWhenSet(t *testing.T) {
	cfg := testConfig("http://internal.example.invalid:9000")
	cfg.PublicEndpoint = "http://public.example.invalid:9000"
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	putURL, err := s.PresignPut(context.Background(), ports.PresignPutSpec{Key: "k", TTL: time.Minute})
	if err != nil {
		t.Fatalf("PresignPut() error = %v", err)
	}
	if !strings.HasPrefix(putURL.URL, "http://public.example.invalid:9000/") {
		t.Errorf("PresignPut() URL = %q, want it to start with the PublicEndpoint host", putURL.URL)
	}
	if strings.Contains(putURL.URL, "internal.example.invalid") {
		t.Errorf("PresignPut() URL = %q, must never be signed against the internal Endpoint host when PublicEndpoint is set", putURL.URL)
	}

	getURL, err := s.PresignGet(context.Background(), ports.PresignGetSpec{Key: "k", TTL: time.Minute})
	if err != nil {
		t.Fatalf("PresignGet() error = %v", err)
	}
	if !strings.HasPrefix(getURL.URL, "http://public.example.invalid:9000/") {
		t.Errorf("PresignGet() URL = %q, want it to start with the PublicEndpoint host", getURL.URL)
	}
}

func TestPresignPutGet_FallBackToEndpointWhenPublicEndpointEmpty(t *testing.T) {
	cfg := testConfig("http://only-endpoint.example.invalid:9000")
	// PublicEndpoint deliberately left empty.
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	putURL, err := s.PresignPut(context.Background(), ports.PresignPutSpec{Key: "k", TTL: time.Minute})
	if err != nil {
		t.Fatalf("PresignPut() error = %v", err)
	}
	if !strings.HasPrefix(putURL.URL, "http://only-endpoint.example.invalid:9000/") {
		t.Errorf("PresignPut() URL = %q, want it to fall back to Endpoint when PublicEndpoint is empty", putURL.URL)
	}
}

// --- flattenHeader ---

func TestFlattenHeader(t *testing.T) {
	h := map[string][]string{
		"Content-Type":   {"image/png"},
		"Multi":          {"first", "second"},
		"Empty-Value-Ok": {},
	}
	got := flattenHeader(h)
	if got["Content-Type"] != "image/png" {
		t.Errorf("flattenHeader()[Content-Type] = %q, want %q", got["Content-Type"], "image/png")
	}
	if got["Multi"] != "first" {
		t.Errorf("flattenHeader()[Multi] = %q, want the FIRST value %q", got["Multi"], "first")
	}
	if _, ok := got["Empty-Value-Ok"]; ok {
		t.Error("flattenHeader() included a key with zero values, want it skipped")
	}
}
