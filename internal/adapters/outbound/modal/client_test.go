package modal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// TestDo_EncodeError exercises the ENCODE_ERROR branch: a reqBody that
// json.Marshal cannot encode (a channel) fails before any request is
// sent.
func TestDo_EncodeError(t *testing.T) {
	p, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = p.do(context.Background(), ports.OpCreateSandbox, http.MethodPost, "/v1/sandboxes", make(chan int), nil)
	if err == nil {
		t.Fatal("do() error = nil, want an ENCODE_ERROR ProviderError")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("do() error = %v, want *ports.ProviderError", err)
	}
	if pe.Code != "ENCODE_ERROR" {
		t.Errorf("do() error.Code = %q, want %q", pe.Code, "ENCODE_ERROR")
	}
	if pe.Transient {
		t.Error("do() error.Transient = true, want false (a bad request body will never succeed on retry)")
	}
}

// TestProvider_CreateSandbox_MalformedResponseJSON exercises the
// DECODE_ERROR branch: Modal returns a 2xx status but a body that is not
// valid JSON for sandboxResponse.
func TestProvider_CreateSandbox_MalformedResponseJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a DECODE_ERROR ProviderError")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Code != "DECODE_ERROR" {
		t.Errorf("CreateSandbox() error.Code = %q, want %q", pe.Code, "DECODE_ERROR")
	}
	if !pe.Transient {
		t.Error("CreateSandbox() error.Transient = false, want true (a malformed-but-2xx response may be a transient server glitch)")
	}
}
