package upload_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/upload"
)

func TestEvaluateUploadSize(t *testing.T) {
	const (
		maxUpload        = int64(100)
		maxSessionUpload = int64(250)
	)

	tests := []struct {
		name              string
		sizeBytes         int64
		sessionTotalBytes int64
		wantReason        upload.FailureReason
		wantOK            bool
	}{
		{
			name:              "well within both limits",
			sizeBytes:         10,
			sessionTotalBytes: 0,
			wantReason:        "",
			wantOK:            true,
		},
		{
			name:              "exactly at the per-file cap passes",
			sizeBytes:         maxUpload,
			sessionTotalBytes: 0,
			wantReason:        "",
			wantOK:            true,
		},
		{
			name:              "one byte over the per-file cap fails size_exceeded",
			sizeBytes:         maxUpload + 1,
			sessionTotalBytes: 0,
			wantReason:        upload.FailureReasonSizeExceeded,
			wantOK:            false,
		},
		{
			name:              "exactly at the session cap (existing total + this one) passes",
			sizeBytes:         50,
			sessionTotalBytes: 200,
			wantReason:        "",
			wantOK:            true,
		},
		{
			name:              "one byte over the session cap fails quota_exceeded",
			sizeBytes:         50,
			sessionTotalBytes: 201,
			wantReason:        upload.FailureReasonQuotaExceeded,
			wantOK:            false,
		},
		{
			name:              "size_exceeded takes priority over quota_exceeded when both would fire",
			sizeBytes:         maxUpload + 1,
			sessionTotalBytes: maxSessionUpload, // already at the session cap too
			wantReason:        upload.FailureReasonSizeExceeded,
			wantOK:            false,
		},
		{
			name:              "zero-byte file passes (mint validates non-zero separately, this function does not)",
			sizeBytes:         0,
			sessionTotalBytes: 0,
			wantReason:        "",
			wantOK:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ok := upload.EvaluateUploadSize(tt.sizeBytes, maxUpload, tt.sessionTotalBytes, maxSessionUpload)
			if ok != tt.wantOK {
				t.Errorf("EvaluateUploadSize(...) ok = %v, want %v", ok, tt.wantOK)
			}
			if reason != tt.wantReason {
				t.Errorf("EvaluateUploadSize(...) reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
