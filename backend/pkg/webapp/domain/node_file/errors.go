package node_file

import "errors"

// Domain errors for tracked files. They live here rather than in app_errors
// because the path policy is pure domain logic with no external dependencies —
// the agent enforces the same rules, and the HTTP layer maps these to 400/403.
var (
	// ErrInvalidPath — the path is not a usable absolute, canonical file path.
	ErrInvalidPath = errors.New("invalid file path")
	// ErrRefusedPath — a well-formed path the platform will not track.
	ErrRefusedPath = errors.New("path cannot be tracked")
	// ErrProtectedPath — an operation was attempted that a protected file does
	// not allow (untrack, delete on device).
	ErrProtectedPath = errors.New("file is managed by the platform; it can be edited and applied, but not untracked or deleted from the device")
	// ErrAccessManagedPath — a write, patch or delete of a file the device's
	// access configuration writes.
	ErrAccessManagedPath = errors.New("file is written by the node's access configuration; change the access entries instead")
	// ErrAlreadyTracked — the path is already tracked for this node.
	ErrAlreadyTracked = errors.New("file is already tracked")
	// ErrNotTracked — no tracked file for this node and path.
	ErrNotTracked = errors.New("file is not tracked")
	// ErrTrackedLimit — the node is at its tracked-file cap.
	ErrTrackedLimit = errors.New("tracked file limit reached for this node")
	// ErrFileTooLarge — content exceeds the configured per-file cap.
	ErrFileTooLarge = errors.New("file is too large to track")
	// ErrBinaryContent — the file is not valid UTF-8 text.
	ErrBinaryContent = errors.New("file is binary")
	// ErrContentUnreadable — the stored content could not be decrypted. It is
	// deliberately an error rather than an empty string: an empty editor would
	// invite an Apply that writes an empty file to the device.
	ErrContentUnreadable = errors.New("stored content could not be decrypted")
)
