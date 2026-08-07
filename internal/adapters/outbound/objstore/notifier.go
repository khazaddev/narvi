package objstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// BlobDeletePayload is the JSON payload shape internal/app/outboxworker
// expects to find in an outbox entry's own payload column for a
// "blob_delete" outbox kind (§28.4): confirm-time verification failure
// (an upload whose declared size/content-type never matched what Stat
// finds, or that was never confirmed at all before its window lapsed)
// enqueues one of these so the half-uploaded object is eventually reaped
// -- even across a crash between the status write and the delete. Key
// travels here (rather than being looked up fresh at delivery time)
// because it is NOT a secret -- an opaque BlobKey carries zero
// client-controlled bytes (§28.3) and zero credential material, unlike a
// decrypted OAuth/API token (internal/app/outboxworker's own doc comment:
// "a decrypted token must never sit in the outbox payload at rest" -- a
// rule this payload never touches).
type BlobDeletePayload struct {
	Key string `json:"key"`
}

// BlobDeleteNotifier delivers the "blob_delete" outbox kind by calling
// Store.Delete, which is itself idempotent (ports.BlobStore's own doc
// comment: "deleting an already-absent key succeeds") -- a redelivered
// attempt, whether from a transient failure or from a crash between a
// successful delivery and the outbox mark-delivered write, is always
// safe to retry. Mirrors rwx.PreviewNotifier's own "one Deliver call,
// wrapping a single already-constructed dependency" shape, and
// rwx.PreviewNotifier's own doc comment on why redelivery is safe for its
// own outbox kind.
//
// internal/app/outboxworker.Builder.attempt (builder.go) treats ANY
// non-nil Deliver error identically, purely by attempt count
// (domain/outbox.EvaluateBackoff) -- it never inspects the error's own
// shape or calls ports.IsBlobStoreTransient. Deliver below therefore
// returns whatever Store.Delete produces as-is (already correctly
// classified internally, for callers that DO branch on transience) rather
// than reclassifying or discarding that detail.
type BlobDeleteNotifier struct {
	store *Store
}

// var _ ports.Notifier = (*BlobDeleteNotifier)(nil) makes a Notifier
// signature drift a build error, not a runtime surprise.
var _ ports.Notifier = (*BlobDeleteNotifier)(nil)

// NewBlobDeleteNotifier builds a BlobDeleteNotifier wrapping store.
func NewBlobDeleteNotifier(store *Store) *BlobDeleteNotifier {
	return &BlobDeleteNotifier{store: store}
}

// Deliver implements ports.Notifier: decodes notification.Payload as
// BlobDeletePayload and deletes that one object. notification.Kind is not
// checked -- this BlobDeleteNotifier is only ever asked to Deliver
// "blob_delete" rows in practice (the delivery worker's own kind->Notifier
// routing is what guarantees that, mirroring every other Notifier
// implementation in this codebase, e.g. rwx.PreviewNotifier).
func (n *BlobDeleteNotifier) Deliver(ctx context.Context, notification ports.Notification) error {
	var payload BlobDeletePayload
	if err := json.Unmarshal(notification.Payload, &payload); err != nil {
		return fmt.Errorf("objstore: decode blob delete payload: %w", err)
	}

	if err := n.store.Delete(ctx, ports.BlobKey(payload.Key)); err != nil {
		return fmt.Errorf("objstore: deliver blob delete: %w", err)
	}
	return nil
}
