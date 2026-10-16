package ownedkind

// CleanupError retains a cause for errors.Is/As, but exposes only fixed codes
// and counts. Docker output, resource names and credentials never enter Error.
type CleanupError struct {
	reason                      string
	nodesRemoved, imagesRemoved int
	cause                       error
}

func (e *CleanupError) Error() string { return "owned Kind cleanup failed" }
func (e *CleanupError) Unwrap() error { return e.cause }

// Diagnostic returns a fixed stage/reason pair. Revalidate even typed errors
// at this boundary so a zero value cannot introduce arbitrary output.
func (e *CleanupError) Diagnostic() (stage, reason string, nodesRemoved, imagesRemoved int) {
	stage, reason = "cleanup", "CLEANUP_FAILED"
	if e == nil {
		return
	}
	switch e.reason {
	case "NODE_LEDGER_INCOMPLETE":
		stage = "cleanup_node_ledger"
	case "NODE_OWNERSHIP_UNCONFIRMED":
		stage = "cleanup_node_ownership"
	case "NODE_DELETION_FAILED":
		stage = "cleanup_node_deletion"
	case "IMAGE_LEDGER_INVALID":
		stage = "cleanup_image_ledger"
	case "IMAGE_LOOKUP_FAILED":
		stage = "cleanup_image_lookup"
	case "IMAGE_INSPECTION_FAILED":
		stage = "cleanup_image_inspection"
	case "IMAGE_OWNERSHIP_CHANGED":
		stage = "cleanup_image_ownership"
	case "IMAGE_DELETION_FAILED":
		stage = "cleanup_image_deletion"
	default:
		return
	}
	reason = e.reason
	nodesRemoved, imagesRemoved = e.nodesRemoved, e.imagesRemoved
	return
}
