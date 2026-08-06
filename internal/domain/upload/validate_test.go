package upload_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/upload"
)

// TestValidateUploadMetadata is table-driven over every branch
// ValidateUploadMetadata/validateMetadataField can take -- the mint-time
// (layer 1) half of the FIX A defense in depth, see validate.go's own doc
// comment for the full rationale and prompt_test.go for layer 2 (render-time
// sanitization).
func TestValidateUploadMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		filename      string
		contentType   string
		wantErr       bool
		wantErrSubstr string
	}{
		{name: "ordinary filename/contentType", filename: "spec.pdf", contentType: "application/pdf", wantErr: false},
		{name: "empty filename/contentType", filename: "", contentType: "", wantErr: false},
		{
			name:          "filename contains newline",
			filename:      "evil\nname.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename contains carriage return",
			filename:      "evil\rname.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename contains tab",
			filename:      "evil\tname.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename contains a C0 control (bell)",
			filename:      "evil\x07name.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename contains a C1 control",
			filename:      "evil" + "\u0085" + "name.txt", // U+0085 NEL, a C1 control
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename contains DEL",
			filename:      "evil\x7fname.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "contentType contains newline",
			filename:      "ok.txt",
			contentType:   "text/plain\n",
			wantErr:       true,
			wantErrSubstr: "control characters",
		},
		{
			name:          "filename exceeds max length",
			filename:      strings.Repeat("a", upload.MaxFilenameBytes+1),
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "maximum length",
		},
		{
			name:        "filename at exactly max length is allowed",
			filename:    strings.Repeat("a", upload.MaxFilenameBytes),
			contentType: "text/plain",
			wantErr:     false,
		},
		{
			name:          "contentType exceeds max length",
			filename:      "ok.txt",
			contentType:   strings.Repeat("a", upload.MaxContentTypeBytes+1),
			wantErr:       true,
			wantErrSubstr: "maximum length",
		},
		{
			name:          "filename is invalid UTF-8",
			filename:      "evil\xff\xfename.txt",
			contentType:   "text/plain",
			wantErr:       true,
			wantErrSubstr: "UTF-8",
		},
		{
			name:        "a literal placeholder token alone is NOT rejected by this layer",
			filename:    "{{UPLOAD_TOOL_BEARER}}.txt",
			contentType: "text/plain",
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := upload.ValidateUploadMetadata(tc.filename, tc.contentType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateUploadMetadata(%q, %q) = nil, want an error", tc.filename, tc.contentType)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("ValidateUploadMetadata(%q, %q) error = %q, want it to contain %q", tc.filename, tc.contentType, err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateUploadMetadata(%q, %q) = %v, want nil", tc.filename, tc.contentType, err)
			}
		})
	}
}
