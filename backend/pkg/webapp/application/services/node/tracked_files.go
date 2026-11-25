package node

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// SetTrackedFileRepository wires the tracked-file store. It is a setter rather
// than a constructor argument for the same reason the gRPC services are: the
// composition root builds this service before the repository exists.
func (s *Service) SetTrackedFileRepository(repo repository.NodeTrackedFileRepository) {
	s.trackedFileRepo = repo
}

// ListTrackedFiles returns the files followed on a node.
func (s *Service) ListTrackedFiles(ctx context.Context, nodeID uuid.UUID) ([]node_file.TrackedFile, error) {
	if s.trackedFileRepo == nil {
		return nil, fmt.Errorf("tracked file repository not available")
	}
	return s.trackedFileRepo.ListByNodeID(ctx, nodeID)
}

// GetTrackedFile returns one tracked file.
func (s *Service) GetTrackedFile(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.TrackedFile, error) {
	if s.trackedFileRepo == nil {
		return nil, fmt.Errorf("tracked file repository not available")
	}
	return s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
}

// ReadNodeFile dispatches a bounded read of one path and returns the command id
// to poll. Used by both "track a new file" and "refresh a tracked file" — the
// difference is only whether a tracked record already exists.
func (s *Service) ReadNodeFile(ctx context.Context, accountID, userID, nodeID uuid.UUID, path string, maxBytes int64) (uuid.UUID, error) {
	if s.trackedFileRepo == nil {
		return uuid.Nil, fmt.Errorf("tracked file repository not available")
	}
	cleanPath, err := node_file.ValidateTrackPath(path)
	if err != nil {
		return uuid.Nil, err
	}
	// The node is still fetched for its preconditions (netboot hold, path
	// policy); its agent version is no longer needed here, because the typed
	// refusal now travels with the batch item instead of being rebuilt from it.
	if _, err := s.assertFileOperationAllowed(ctx, accountID, nodeID); err != nil {
		return uuid.Nil, err
	}

	batch, err := s.SendBatchCommand(withTrackFileRead(ctx, userID), node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugReadFile,
		Params: map[string]interface{}{
			"path":      cleanPath,
			"max_bytes": float64(maxBytes),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, item.FailureError()
	}
	if item.CommandID == nil {
		return uuid.Nil, fmt.Errorf("the read was queued but not dispatched (node offline)")
	}
	return *item.CommandID, nil
}

// trackFileReadKey marks a context whose read_file dispatch belongs to the
// tracked-file flow; the value is the user who asked.
type trackFileReadKey struct{}

func withTrackFileRead(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, trackFileReadKey{}, userID)
}

func trackFileReadUser(ctx context.Context) (uuid.UUID, bool) {
	userID, ok := ctx.Value(trackFileReadKey{}).(uuid.UUID)
	return userID, ok
}

// CompleteTrackedFileRead records a tracked-file read when the device's
// result arrives: the content becomes a version and the tracked record is
// created or refreshed, exactly as a poll of GetNodeFileReadResult would do.
// Only a read the tracked-file flow dispatched (its payload names the user,
// results.TrackFileForKey) is recorded; any other read_file — a playbook
// block, the generic command route — tracks nothing.
//
// Tracking used to complete only inside the poll, so a browser closed between
// the dispatch and the result left a read that succeeded on the device and a
// file the platform never recorded. The poll still works and is idempotent
// over this: the content is stored once.
func (s *Service) CompleteTrackedFileRead(ctx context.Context, cmd *node_command.NodeCommand, result map[string]string) error {
	if cmd == nil || cmd.Slug != node_command.SlugReadFile || s.trackedFileRepo == nil || s.fileService == nil {
		return nil
	}
	marker, _ := cmd.Payload[results.TrackFileForKey].(string)
	if marker == "" {
		return nil
	}
	userID, err := uuid.Parse(marker)
	if err != nil {
		return fmt.Errorf("tracked-file read %s carries an unreadable user: %w", cmd.ID, err)
	}
	cleanPath, err := node_file.ValidateTrackPath(pathOf(cmd))
	if err != nil {
		return err
	}
	maxBytes := int64(node_file.MaxContentBytes)
	if v, ok := cmd.Payload["max_bytes"].(float64); ok && v > 0 {
		maxBytes = int64(v)
	}
	out, err := s.storeReadResult(ctx, cmd.AccountID, userID, cmd.NodeID, cleanPath, result, maxBytes, s.fileLimits().FilesPerNode)
	if err != nil {
		return err
	}
	if out.Status != "completed" {
		s.logger.InfoContext(ctx, "a tracked-file read was not recorded",
			"node_id", cmd.NodeID, "path", cleanPath, "reason", out.Detail)
	}
	return nil
}

// GetNodeFileReadResult polls a dispatched read and, on success, stores the
// content as a new version and creates or updates the tracked-file record.
// The command-result settle normally did that already (CompleteTrackedFileRead);
// storing is idempotent, and "changed" is reported from when the version was
// created, so a poll that arrives after the settle still says whether this
// read added a version.
//
// The command is bound to this node, this slug and this exact path: an unbound
// poll would let a caller read any prior command's decrypted result, which is
// how workflow output redaction gets bypassed.
func (s *Service) GetNodeFileReadResult(
	ctx context.Context,
	accountID, userID, nodeID, commandID uuid.UUID,
	path string,
	maxBytes int64,
	trackedLimit int,
) (*node_file.ReadResult, error) {
	if s.nodeCommandService == nil || s.trackedFileRepo == nil {
		return nil, fmt.Errorf("node command service not available")
	}
	cleanPath, err := node_file.ValidateTrackPath(path)
	if err != nil {
		return nil, err
	}

	cmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, accountID, commandID)
	if err != nil {
		return nil, err
	}
	if cmd.NodeID != nodeID || cmd.Slug != node_command.SlugReadFile || pathOf(cmd) != cleanPath {
		return nil, app_errors.ErrNodeNotFound
	}

	switch cmd.Status {
	case node_command.StatusSuccess:
		out, err := s.storeReadResult(ctx, accountID, userID, nodeID, cleanPath, cmd.Result, maxBytes, trackedLimit)
		if err != nil {
			return nil, err
		}
		// When the settle stored the content first, this call found it
		// already there. The version it names was still added by this read
		// if it was created after the command was.
		if out.Status == "completed" && !out.Changed && out.versionCreatedAt != nil &&
			!cmd.CreatedAt.IsZero() && !out.versionCreatedAt.Before(cmd.CreatedAt) {
			out.Changed = true
		}
		return &out.ReadResult, nil
	case node_command.StatusFailed, node_command.StatusExpired, node_command.StatusSkipped, node_command.StatusSuperseded:
		detail := cmd.Details
		if detail == "" {
			detail = "the file could not be read from the device"
		}
		return &node_file.ReadResult{Status: "failed", Detail: detail, Path: cleanPath}, nil
	default:
		return &node_file.ReadResult{Status: "running", Path: cleanPath}, nil
	}
}

// storedRead is a read's outcome plus when the version it names was created.
type storedRead struct {
	node_file.ReadResult
	versionCreatedAt *time.Time
}

// storeReadResult turns a successful read into a version + tracked-file record.
func (s *Service) storeReadResult(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	path string,
	result map[string]string,
	maxBytes int64,
	trackedLimit int,
) (*storedRead, error) {
	out := &storedRead{ReadResult: node_file.ReadResult{Status: "completed", Path: path}}

	if result["truncated"] == "true" {
		out.Status = "failed"
		out.Truncated = true
		out.Detail = fmt.Sprintf("the file is larger than the %d KB limit and was not stored", maxBytes/1024)
		return out, nil
	}
	if result["binary"] == "true" {
		out.Status = "failed"
		out.Detail = "the file is binary; only text files can be tracked"
		return out, nil
	}

	content, ok := result["content"]
	if !ok {
		// An agent older than the metadata support returns content without the
		// companion fields; that is fine. A result with no content at all is not.
		out.Status = "failed"
		out.Detail = "the device returned no content"
		return out, nil
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		out.Status = "failed"
		out.Detail = fmt.Sprintf("the file is larger than the %d KB limit", maxBytes/1024)
		return out, nil
	}
	if !utf8.ValidString(content) {
		out.Status = "failed"
		out.Detail = "the file is not valid UTF-8 text"
		return out, nil
	}

	source := node_file.SourceUser
	if node_file.IsPlatformPath(path) {
		source = node_file.SourcePlatform
	}
	tracked, file, changed, err := s.recordAppliedVersion(ctx, accountID, userID, nodeID, path, content, source, trackedLimit)
	if err != nil {
		return nil, err
	}
	out.Version = file.Version
	out.Changed = changed
	if !file.CreatedAt.IsZero() {
		created := file.CreatedAt
		out.versionCreatedAt = &created
	}

	now := time.Now()
	tracked.LastReadAt = &now
	tracked.Size = int64(len(content))
	if m := result["mode"]; m != "" {
		tracked.Mode = m
	}
	if o := result["owner"]; o != "" {
		tracked.Owner = o
	}
	if g := result["group"]; g != "" {
		tracked.Group = g
	}
	if err := s.trackedFileRepo.Update(ctx, tracked); err != nil {
		return nil, err
	}

	out.Size = tracked.Size
	out.Mode = tracked.Mode
	out.Owner = tracked.Owner
	out.Group = tracked.Group
	return out, nil
}

// recordAppliedVersion is the shared get-or-create + budget + version + applied
// path behind storing content that the device itself vouches for: a read off
// the device, or the rendered text a successful apply reported back. It creates
// the tracked record when needed (enforcing the per-node file cap for new
// tracks), enforces the storage budget, stores the content as a version, and
// marks it applied — what came from the device IS what the device has.
// Callers finish by updating the tracked record's metadata and persisting it.
func (s *Service) recordAppliedVersion(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	path, content, source string,
	trackedLimit int,
) (*node_file.TrackedFile, *node_file.NodeFile, bool, error) {
	tracked, err := s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
	switch {
	case err == nil:
		// refresh of an existing track
	case isNotTracked(err):
		// Budget first, then the track. Creating the row and finding out
		// afterwards that the content does not fit left an orphan behind: a
		// tracked file with no version, listed as "never read", counting against
		// the node's file cap for a read that never happened.
		if berr := s.assertStorageBudget(ctx, accountID, nodeID, int64(len(content))); berr != nil {
			return nil, nil, false, berr
		}
		// The cap check and the insert belong in one atomic step. Counting here
		// and creating afterwards let two in-flight polls for different paths
		// both see the node below the limit and both insert, so the cap could be
		// exceeded by however many polls were open at once.
		tracked, err = s.trackedFileRepo.CreateUnderLimit(
			ctx, node_file.NewTrackedFile(accountID, nodeID, path, source, userID), trackedLimit)
		if err != nil {
			return nil, nil, false, err
		}
	default:
		return nil, nil, false, err
	}

	if berr := s.assertStorageBudget(ctx, accountID, nodeID, int64(len(content))); berr != nil {
		return nil, nil, false, berr
	}

	// Store the content as a version. CreateVersion is idempotent on identical
	// content, so a refresh that finds no change does not grow the history.
	before, _ := s.fileService.GetLatest(ctx, nodeID, path)
	file, err := s.fileService.CreateVersion(ctx, nodeID, path, content)
	if err != nil {
		return nil, nil, false, err
	}
	changed := before == nil || before.Hash != file.Hash

	// A version the device vouches for is by definition what the device has.
	if file.Status != node_file.StatusApplied {
		if err := s.fileService.UpdateStatus(ctx, nodeID, path, file.Version, node_file.StatusApplied, nil); err != nil {
			s.logger.Warn("failed to mark the read version as applied", "node_id", nodeID, "path", path, "error", err)
		}
	}
	return tracked, file, changed, nil
}

// RecordPlatformRenderedFile stores content the platform itself put on the node
// (via the agent) as a tracked-file version: source=platform, initiating user
// unknown (uuid.Nil — node commands do not record their user), status applied.
// Used for the rendered /etc/watchdog.conf a successful watchdog apply reports
// back. The caller must treat a failure as log-and-continue: recording the copy
// is bookkeeping, never part of the apply itself.
func (s *Service) RecordPlatformRenderedFile(ctx context.Context, accountID, nodeID uuid.UUID, path, content string) error {
	if s.trackedFileRepo == nil {
		return fmt.Errorf("tracked file repository not available")
	}
	cleanPath, err := node_file.ValidateTrackPath(path)
	if err != nil {
		return err
	}
	tracked, _, _, err := s.recordAppliedVersion(
		ctx, accountID, uuid.Nil, nodeID, cleanPath, content,
		node_file.SourcePlatform, s.fileLimits().FilesPerNode,
	)
	if err != nil {
		return err
	}
	now := time.Now()
	tracked.LastReadAt = &now
	tracked.LastAppliedAt = &now
	tracked.Size = int64(len(content))
	return s.trackedFileRepo.Update(ctx, tracked)
}

// SaveNodeFileVersion stores an edited version of a tracked file.
//
// This is the path the editor's Save takes, and it used to go straight to the
// version store: no check that the file was tracked at all, so a caller could
// create rows for thousands of paths and walk past the per-node file cap
// entirely, and no budget, so the per-file limit multiplied by versions and by
// paths with nothing at the end of it.
func (s *Service) SaveNodeFileVersion(
	ctx context.Context,
	accountID, nodeID uuid.UUID,
	path, content string,
) (*node_file.NodeFile, error) {
	if s.trackedFileRepo == nil {
		return nil, fmt.Errorf("tracked file repository not available")
	}
	tracked, err := s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
	if err != nil {
		return nil, err
	}
	if tracked.AccountID != uuid.Nil && tracked.AccountID != accountID {
		return nil, app_errors.ErrNodeNotFound
	}
	if err := s.assertStorageBudget(ctx, accountID, nodeID, int64(len(content))); err != nil {
		return nil, err
	}
	return s.fileService.CreateVersion(ctx, nodeID, path, content)
}

// MarkTrackedFileApplied records that the device confirmed a write of this
// file. Called from the command settle, the only point where the platform knows
// the write actually landed — a dispatch is not an apply.
func (s *Service) MarkTrackedFileApplied(ctx context.Context, accountID, nodeID uuid.UUID, path string) error {
	if s.trackedFileRepo == nil {
		return nil
	}
	tracked, err := s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
	if err != nil {
		if isNotTracked(err) {
			// A write to a path nobody follows (a workflow block, say) is not an
			// error here: there is simply no record to stamp.
			return nil
		}
		return err
	}
	if tracked.AccountID != uuid.Nil && accountID != uuid.Nil && tracked.AccountID != accountID {
		return app_errors.ErrNodeNotFound
	}
	now := time.Now()
	tracked.LastAppliedAt = &now
	return s.trackedFileRepo.Update(ctx, tracked)
}

// assertStorageBudget refuses a write that would take a node — or the whole
// account — past its stored-content budget. Counted from the table, so pruning
// and untracking free the space back up rather than the number only ever
// climbing.
//
// The account ceiling exists because the per-node budget multiplies: a thousand
// nodes at 16 MB each is 16 GB from one tenant. It is unlimited by default, so
// an install that has not chosen a number is unaffected.
func (s *Service) assertStorageBudget(ctx context.Context, accountID, nodeID uuid.UUID, incoming int64) error {
	limits := s.fileLimits()
	if limits.BytesPerNode > 0 {
		stored, err := s.fileService.StoredBytes(ctx, nodeID)
		if err != nil {
			return err
		}
		if stored+incoming > limits.BytesPerNode {
			return fmt.Errorf("%w: this node already stores %d KB of file content and the budget is %d KB; untrack a file or remove old versions",
				node_file.ErrTrackedLimit, stored/1024, limits.BytesPerNode/1024)
		}
	}
	if limits.BytesPerAccount > 0 && accountID != uuid.Nil {
		stored, err := s.fileService.StoredBytesForAccount(ctx, accountID)
		if err != nil {
			return err
		}
		if stored+incoming > limits.BytesPerAccount {
			return fmt.Errorf("%w: this account already stores %d KB of file content across its nodes and the budget is %d KB; untrack files or remove old versions",
				node_file.ErrTrackedLimit, stored/1024, limits.BytesPerAccount/1024)
		}
	}
	return nil
}

// fileLimits returns the configured tracked-file quotas, falling back to the
// shipped defaults when nothing wired them. Never zero-value: an unwired
// service must behave as the feature always has, not as unlimited.
func (s *Service) fileLimits() node_file.Limits {
	if s.nodeFileLimits == nil {
		return node_file.DefaultLimits()
	}
	return *s.nodeFileLimits
}

// SetNodeFileLimits wires the install's tracked-file quotas.
func (s *Service) SetNodeFileLimits(limits node_file.Limits) {
	s.nodeFileLimits = &limits
}

// UntrackNodeFile forgets a file and its version history. The device is not
// touched. Protected files cannot be untracked — the platform put them there.
func (s *Service) UntrackNodeFile(ctx context.Context, accountID, nodeID uuid.UUID, path string) error {
	if s.trackedFileRepo == nil {
		return fmt.Errorf("tracked file repository not available")
	}
	tracked, err := s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
	if err != nil {
		return err
	}
	if tracked.AccountID != uuid.Nil && tracked.AccountID != accountID {
		return app_errors.ErrNodeNotFound
	}
	if tracked.IsProtected() {
		return fmt.Errorf("%w: %s", node_file.ErrProtectedPath, path)
	}
	if err := s.fileService.DeleteVersions(ctx, nodeID, path); err != nil {
		return err
	}
	return s.trackedFileRepo.Delete(ctx, tracked.ID)
}

// DeleteNodeFileOnDevice dispatches the agent's delete_file for a tracked file
// and, on dispatch, untracks it. The typed confirmation is verified here, not
// only in the browser: the destructive verb must not be reachable by a caller
// that skips the UI.
func (s *Service) DeleteNodeFileOnDevice(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	path, confirmPath string,
	backup bool,
) (uuid.UUID, error) {
	if s.trackedFileRepo == nil {
		return uuid.Nil, fmt.Errorf("tracked file repository not available")
	}
	if confirmPath != path {
		return uuid.Nil, fmt.Errorf("%w: the confirmation does not match the file path", node_file.ErrInvalidPath)
	}
	tracked, err := s.trackedFileRepo.GetByNodeIDAndPath(ctx, nodeID, path)
	if err != nil {
		return uuid.Nil, err
	}
	if tracked.IsProtected() {
		return uuid.Nil, fmt.Errorf("%w: %s", node_file.ErrProtectedPath, path)
	}
	// The node is still fetched for its preconditions (netboot hold, path
	// policy); its agent version is no longer needed here, because the typed
	// refusal now travels with the batch item instead of being rebuilt from it.
	if _, err := s.assertFileOperationAllowed(ctx, accountID, nodeID); err != nil {
		return uuid.Nil, err
	}

	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugDeleteFile,
		Params: map[string]interface{}{
			"path":   path,
			"backup": backup,
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, item.FailureError()
	}
	if item.CommandID == nil {
		return uuid.Nil, fmt.Errorf("the delete was queued but not dispatched (node offline)")
	}
	return *item.CommandID, nil
}

// assertFileOperationAllowed refuses file operations against a node whose
// filesystem is not the one the operator is looking at.
//
// A netbooted node runs a Buildroot initramfs: reading /etc/hosts there returns
// the bootstrap's file, and writing or deleting evaporates on the next reboot
// while reporting success. This is a synchronous check on purpose —
// RequiresManagedOS only fires for queueable commands, and none of these are
// (a file operation applied hours later is a surprise, not a convenience).
func (s *Service) assertFileOperationAllowed(ctx context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	if n.IsRecoveryBootstrap() {
		return nil, fmt.Errorf("the node is running the netboot recovery image; its installed filesystem is not mounted")
	}
	return n, nil
}

// pathOf returns a command payload's path (empty if absent).
func pathOf(cmd *node_command.NodeCommand) string {
	if cmd == nil || cmd.Payload == nil {
		return ""
	}
	p, _ := cmd.Payload["path"].(string)
	return p
}

// isNotTracked reports whether the error means "no such tracked file".
func isNotTracked(err error) bool {
	return errors.Is(err, node_file.ErrNotTracked)
}
