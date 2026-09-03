package upload_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/domain/upload"
)

func TestBuildBlobKey(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		uploadID  string
		want      ports.BlobKey
	}{
		{
			name:      "typical uuids",
			sessionID: "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a",
			uploadID:  "9b1a6b1a-4b1a-9b1a-6b1a-5b1c1e2e6b1a",
			want:      "sessions/5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a/uploads/9b1a6b1a-4b1a-9b1a-6b1a-5b1c1e2e6b1a",
		},
		{
			name:      "empty inputs still produce a deterministic key (no panic, no I/O)",
			sessionID: "",
			uploadID:  "",
			want:      "sessions//uploads/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upload.BuildBlobKey(tt.sessionID, tt.uploadID)
			if got != tt.want {
				t.Errorf("BuildBlobKey(%q, %q) = %q, want %q", tt.sessionID, tt.uploadID, got, tt.want)
			}
		})
	}
}

// TestBuildBlobKey_NoClientControlledBytes proves the key carries only its
// two UUID inputs and the fixed convention text -- §28.3's "zero
// client-controlled bytes" invariant is a property of the CALLER (never
// passing a filename/user text into sessionID/uploadID), but this test at
// least pins that the function itself introduces nothing beyond its two
// inputs verbatim (no normalization, no injected separators a caller
// could exploit to fake a different path).
func TestBuildBlobKey_NoClientControlledBytes(t *testing.T) {
	got := upload.BuildBlobKey("SID", "UID")
	want := ports.BlobKey("sessions/SID/uploads/UID")
	if got != want {
		t.Errorf("BuildBlobKey(%q, %q) = %q, want %q", "SID", "UID", got, want)
	}
}
