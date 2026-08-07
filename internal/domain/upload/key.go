package upload

import "github.com/khazaddev/narvi/internal/app/ports"

// BuildBlobKey is the ONLY place in this codebase that produces a
// ports.BlobKey (§28.1: "BlobKey is opaque to the adapter: only the CP's
// own key builder produces one; the adapter never parses or constructs
// keys") -- the fixed "sessions/{session_id}/uploads/{upload_id}"
// convention (§28.3), where uploadID is the artifact row's own UUID.
//
// Zero client-controlled bytes: no filename, no user text rides the key
// (the filename lives on the artifacts row and is applied at download
// time via response-content-disposition, §28.5) -- both sessionID and
// uploadID are server-generated UUIDs, so path traversal, encoding
// surprises, and collision games are unrepresentable rather than
// validated away.
func BuildBlobKey(sessionID, uploadID string) ports.BlobKey {
	return ports.BlobKey("sessions/" + sessionID + "/uploads/" + uploadID)
}
