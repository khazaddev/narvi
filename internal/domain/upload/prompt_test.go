package upload_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/upload"
)

func TestRenderAttachmentBlock_Empty(t *testing.T) {
	tests := [][]upload.AttachmentInfo{
		nil,
		{},
	}
	for _, attachments := range tests {
		if got := upload.RenderAttachmentBlock(attachments); got != "" {
			t.Errorf("RenderAttachmentBlock(%#v) = %q, want empty string (byte-for-byte no-op)", attachments, got)
		}
	}
}

func TestRenderAttachmentBlock_SingleAttachment(t *testing.T) {
	attachments := []upload.AttachmentInfo{
		{
			SessionID:   "sess-1",
			UploadID:    "up-1",
			Filename:    "spec.pdf",
			SizeBytes:   4096,
			ContentType: "application/pdf",
		},
	}

	got := upload.RenderAttachmentBlock(attachments)

	for _, want := range []string{
		"spec.pdf",
		"4096 bytes",
		"application/pdf",
		"curl -fL",
		"Authorization: Bearer " + upload.BearerPlaceholder,
		"X-Sandbox-Gen: " + upload.GenPlaceholder,
		upload.BaseURLPlaceholder + "/sessions/sess-1/uploads/up-1/content",
		"<upload_attachments>",
		"</upload_attachments>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAttachmentBlock(...) = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderAttachmentBlock_MultipleAttachmentsEachGetOwnPath(t *testing.T) {
	attachments := []upload.AttachmentInfo{
		{SessionID: "s1", UploadID: "u1", Filename: "a.png", SizeBytes: 1, ContentType: "image/png"},
		{SessionID: "s1", UploadID: "u2", Filename: "b.csv", SizeBytes: 2, ContentType: "text/csv"},
	}

	got := upload.RenderAttachmentBlock(attachments)

	if !strings.Contains(got, upload.BaseURLPlaceholder+"/sessions/s1/uploads/u1/content") {
		t.Errorf("RenderAttachmentBlock(...) missing first attachment's own path: %q", got)
	}
	if !strings.Contains(got, upload.BaseURLPlaceholder+"/sessions/s1/uploads/u2/content") {
		t.Errorf("RenderAttachmentBlock(...) missing second attachment's own path: %q", got)
	}
	// Every attachment shares the SAME bearer/gen placeholder -- only one
	// live substitution resolves both, never a per-attachment secret.
	if strings.Count(got, upload.BearerPlaceholder) != 2 {
		t.Errorf("RenderAttachmentBlock(...) = %q, want the shared bearer placeholder to appear once per attachment", got)
	}
}

// TestRenderAttachmentBlock_UntrustedFieldsAreDelimited proves filename/
// content-type -- attacker/user-supplied at mint time -- are rendered only
// inside the fixed <upload_attachments> delimiter, matching §5.2's "wrap
// untrusted content in delimited blocks" discipline.
func TestRenderAttachmentBlock_UntrustedFieldsAreDelimited(t *testing.T) {
	attachments := []upload.AttachmentInfo{
		{SessionID: "s", UploadID: "u", Filename: "evil.txt", SizeBytes: 1, ContentType: "text/plain"},
	}
	got := upload.RenderAttachmentBlock(attachments)

	openIdx := strings.Index(got, "<upload_attachments>")
	closeIdx := strings.Index(got, "</upload_attachments>")
	filenameIdx := strings.Index(got, "evil.txt")

	if openIdx == -1 || closeIdx == -1 || filenameIdx == -1 {
		t.Fatalf("RenderAttachmentBlock(...) = %q, missing expected markers", got)
	}
	if !(openIdx < filenameIdx && filenameIdx < closeIdx) {
		t.Errorf("RenderAttachmentBlock(...) = %q, want filename rendered strictly between the delimiter tags", got)
	}
}

func TestRenderUploadToolNote_AlwaysNonEmptyAndSessionScoped(t *testing.T) {
	got := upload.RenderUploadToolNote("sess-42")

	if got == "" {
		t.Fatal("RenderUploadToolNote(...) = \"\", want a non-empty, unconditional note")
	}

	for _, want := range []string{
		"POST " + upload.BaseURLPlaceholder + "/sessions/sess-42/uploads",
		"/sessions/sess-42/uploads/<uploadId>/complete",
		"putUrl",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderUploadToolNote(...) = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderUploadToolNote_DifferentSessionsGetDifferentPaths(t *testing.T) {
	a := upload.RenderUploadToolNote("sess-a")
	b := upload.RenderUploadToolNote("sess-b")
	if a == b {
		t.Fatal("RenderUploadToolNote for two different sessions produced identical text")
	}
	if !strings.Contains(a, "/sessions/sess-a/uploads") || strings.Contains(a, "/sessions/sess-b/uploads") {
		t.Errorf("RenderUploadToolNote(%q) = %q, want it to reference only its own session", "sess-a", a)
	}
}
