package credentials_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

func TestParseDescriptor_SeparateFields(t *testing.T) {
	t.Parallel()

	input := "protocol=https\nhost=example.com\npath=foo/bar.git\n\n"
	desc, err := credentials.ParseDescriptor(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDescriptor() error = %v, want nil", err)
	}
	want := credentials.Descriptor{Protocol: "https", Host: "example.com", Path: "foo/bar.git"}
	if desc != want {
		t.Errorf("ParseDescriptor() = %+v, want %+v", desc, want)
	}
}

func TestParseDescriptor_URLLine(t *testing.T) {
	t.Parallel()

	input := "url=https://example.com/foo/bar.git\n\n"
	desc, err := credentials.ParseDescriptor(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDescriptor() error = %v, want nil", err)
	}
	want := credentials.Descriptor{Protocol: "https", Host: "example.com", Path: "foo/bar.git"}
	if desc != want {
		t.Errorf("ParseDescriptor() = %+v, want %+v", desc, want)
	}
}

// TestParseDescriptor_URLLineDoesNotOverrideExplicitFields proves an
// explicit protocol/host line wins over a url= line's own derived value
// when both are present (ParseDescriptor only derives from url= when
// protocol/host are themselves absent).
func TestParseDescriptor_URLLineDoesNotOverrideExplicitFields(t *testing.T) {
	t.Parallel()

	input := "protocol=https\nhost=explicit.example.com\nurl=https://from-url.example.com/repo.git\n\n"
	desc, err := credentials.ParseDescriptor(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDescriptor() error = %v, want nil", err)
	}
	if desc.Host != "explicit.example.com" {
		t.Errorf("Host = %q, want the explicit %q to win over the url= line", desc.Host, "explicit.example.com")
	}
}

// TestParseDescriptor_NoTerminatingBlankLine proves EOF alone (no blank
// line) correctly terminates the stream, per git's own protocol ("a blank
// line OR EOF").
func TestParseDescriptor_NoTerminatingBlankLine(t *testing.T) {
	t.Parallel()

	input := "protocol=https\nhost=example.com"
	desc, err := credentials.ParseDescriptor(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDescriptor() error = %v, want nil", err)
	}
	if desc.Protocol != "https" || desc.Host != "example.com" {
		t.Errorf("ParseDescriptor() = %+v, want {https example.com}", desc)
	}
}

func TestParseDescriptor_EmptyStream(t *testing.T) {
	t.Parallel()

	desc, err := credentials.ParseDescriptor(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseDescriptor() error = %v, want nil", err)
	}
	if desc != (credentials.Descriptor{}) {
		t.Errorf("ParseDescriptor() = %+v, want the zero Descriptor", desc)
	}
}

func TestParseDescriptor_MalformedLine(t *testing.T) {
	t.Parallel()

	input := "protocol=https\nthis-line-has-no-equals-sign\n\n"
	_, err := credentials.ParseDescriptor(strings.NewReader(input))
	if err == nil {
		t.Fatal("ParseDescriptor() error = nil, want an error for a malformed (non key=value) line")
	}
}
