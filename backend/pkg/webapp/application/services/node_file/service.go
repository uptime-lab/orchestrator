package node_file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// Service handles node file-related business logic
type Service struct {
	repo   repository.NodeFileRepository
	logger *slog.Logger
	// limits are the install's tracked-file quotas. nil means nothing wired
	// them, and retention falls back to the shipped defaults rather than to
	// unlimited — an unwired service must behave as the feature always has.
	limits *node_file.Limits
}

// SetLimits wires the install's tracked-file quotas.
func (s *Service) SetLimits(limits node_file.Limits) {
	s.limits = &limits
}

// bytesPerNodeLimit returns the node byte budget, defaulting like retention
// does: an unwired service behaves as the feature shipped, never as unlimited.
func (s *Service) bytesPerNodeLimit() int64 {
	if s.limits != nil {
		return s.limits.BytesPerNode
	}
	return node_file.DefaultLimits().BytesPerNode
}

// NewService creates a new node file service
func NewService(repo repository.NodeFileRepository, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: logger,
	}
}

// CreateOrUpdateFromAgent creates a new version if hash differs, or updates existing version status
//
// This is DEVICE-authenticated input, not operator input: anything holding a
// node's adoption token can send a FileChangedEvent, so every guard the
// operator paths enforce must hold here too. Before this, an
// event carried an arbitrary path, unbounded content and an unchecked hash —
// rows invisible in the UI (never tracked) and unreclaimable through any API
// could exhaust the node's storage budget.
func (s *Service) CreateOrUpdateFromAgent(ctx context.Context, nodeID uuid.UUID, path, content, hash string, version int, status string, errorMsg *string) (*node_file.NodeFile, error) {
	// Path policy first: same validator as track/read/delete. A guard that
	// lives in one handler is not a guard.
	cleanPath, err := node_file.ValidateTrackPath(path)
	if err != nil {
		return nil, err
	}
	path = cleanPath

	// Bound the content like every other write path. The per-version cap
	// bounds a gRPC frame and a stored row; an agent report is not exempt.
	if len(content) > node_file.MaxContentBytes {
		return nil, fmt.Errorf("%w: agent reported %d bytes for %s (max %d)",
			node_file.ErrInvalidPath, len(content), path, node_file.MaxContentBytes)
	}

	// The hash is the identity later logic dedupes on — an unverified one
	// lets a report alias arbitrary content under another version's identity.
	if content != "" && hash != "" {
		sum := sha256.Sum256([]byte(content))
		if hex.EncodeToString(sum[:]) != hash {
			return nil, fmt.Errorf("%w: reported hash does not match content for %s",
				node_file.ErrInvalidPath, path)
		}
	}

	// If version is 0 (initial report), create new version
	if version == 0 {
		// Same content as the latest version — update its status, never insert a
		// duplicate. The status used to be part of this test (`&& status ==
		// applied`), which meant every failed report added a version row: a
		// single failed apply produced v1 "applying" + v2 "failed" for one file.
		latest, err := s.repo.GetLatestByNodeIDAndPath(ctx, nodeID, path)
		if err == nil && latest.Hash == hash {
			if latest.Status != status || !sameError(latest.ErrorMessage, errorMsg) {
				latest.Status = status
				latest.ErrorMessage = errorMsg
				if err := s.repo.Update(ctx, latest); err != nil {
					return nil, fmt.Errorf("failed to update status: %w", err)
				}
			}
			return latest, nil
		}

		// A report that carries no content cannot create a version: the agent
		// fills content only for the paths it watches, so for every other path
		// it reports an empty string. Creating a version from that would store
		// an empty file that a later Apply writes to the device.
		if content == "" && hash == "" {
			if err == nil {
				if latest.Status != status || !sameError(latest.ErrorMessage, errorMsg) {
					latest.Status = status
					latest.ErrorMessage = errorMsg
					if uerr := s.repo.Update(ctx, latest); uerr != nil {
						return nil, fmt.Errorf("failed to update status: %w", uerr)
					}
				}
				return latest, nil
			}
			return nil, fmt.Errorf("%w: agent reported no content for %s", node_file.ErrInvalidPath, path)
		}

		// A device may CREATE a version only for a path the platform itself
		// watches (the config.txt pair) or one that already has history from
		// the operator's track flow. Without this, a stolen adoption token
		// invented rows under arbitrary allowed paths — each one filling the
		// node's budget while appearing in no list and reclaimable by no API.
		if err != nil && !node_file.IsPlatformPath(path) {
			return nil, fmt.Errorf("%w: device reported an untracked path %s", node_file.ErrNotTracked, path)
		}

		// The node's stored-content budget applies to a device report exactly
		// as it does to an operator write (a FileChangedEvent used to bypass
		// FilesPerNode/BytesPerNode). The FILE-count budget holds structurally
		// on this path — a device can only create versions for platform paths
		// or paths the operator's track flow already admitted under the cap —
		// but the BYTE budget did not: per-version cap (512 KiB) times pruned
		// history (20) times 50 files is ~500 MB against a 16 MB budget, all
		// reachable by an agent re-reporting fresh content. Fails OPEN on a
		// count error like the platform's other count guards: a database blip
		// must not make devices unable to report.
		if limit := s.bytesPerNodeLimit(); limit > 0 {
			stored, serr := s.repo.SumContentBytesByNodeID(ctx, nodeID)
			if serr != nil {
				s.logger.Warn("could not check the node's file-content budget; allowing the report",
					"node_id", nodeID, "error", serr)
			} else if stored+int64(len(content)) > limit {
				return nil, fmt.Errorf("%w: this node already stores %d KB of file content and the budget is %d KB",
					node_file.ErrTrackedLimit, stored/1024, limit/1024)
			}
		}

		// Create new version. The number is allocated by the insert itself — see
		// CreateNextVersion for why reading the maximum first is not safe here.
		file := node_file.NewNodeFile(nodeID, path, content, hash, 0, status)
		file.ErrorMessage = errorMsg

		if err := s.repo.CreateNextVersion(ctx, file); err != nil {
			return nil, fmt.Errorf("failed to create file: %w", err)
		}

		// Prune here too. Retention used to run only in CreateVersion, so history
		// was bounded for a file the operator edited and unbounded for one the
		// agent kept reporting — a path whose versions all arrived from the device
		// kept every one of them. Best effort, for the same reason as there:
		// losing the oldest version is not worth failing the report.
		s.pruneVersions(ctx, nodeID, path)

		return file, nil
	}

	// A report for a specific version can only ever update that version's
	// status. It used to create the row when the version was unknown, which let
	// a stale or malformed report invent history; and it used to overwrite the
	// stored content whenever the reported hash differed, which is how a tracked
	// file silently became empty (the agent reports an empty hash for any path
	// it does not watch). Content is written by the platform, never by a report.
	existing, err := s.repo.GetByNodeIDPathAndVersion(ctx, nodeID, path, version)
	if err != nil {
		return nil, fmt.Errorf("%w: version %d of %s", node_file.ErrNotTracked, version, path)
	}

	existing.Status = status
	existing.ErrorMessage = errorMsg

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("failed to update file: %w", err)
	}

	return existing, nil
}

// sameError compares two optional error messages.
func sameError(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// GetLatest retrieves the latest version of a file
func (s *Service) GetLatest(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.NodeFile, error) {
	return s.repo.GetLatestByNodeIDAndPath(ctx, nodeID, path)
}

// GetAllVersions retrieves all versions of a file
func (s *Service) GetAllVersions(ctx context.Context, nodeID uuid.UUID, path string) ([]node_file.NodeFile, error) {
	return s.repo.GetByNodeIDAndPath(ctx, nodeID, path)
}

// GetByVersion retrieves a specific version of a file
func (s *Service) GetByVersion(ctx context.Context, nodeID uuid.UUID, path string, version int) (*node_file.NodeFile, error) {
	return s.repo.GetByNodeIDPathAndVersion(ctx, nodeID, path, version)
}

// CreateVersion creates a new file version with status "saved" (not yet applied)
func (s *Service) CreateVersion(ctx context.Context, nodeID uuid.UUID, path, content string) (*node_file.NodeFile, error) {
	// Calculate hash
	hashBytes := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(hashBytes[:])

	// Check if latest version has same hash
	latest, err := s.repo.GetLatestByNodeIDAndPath(ctx, nodeID, path)
	if err == nil && latest.Hash == hash {
		// Same content, return existing
		return latest, nil
	}

	// Create new version with status "saved". The number is allocated by the
	// insert — see CreateNextVersion.
	file := node_file.NewNodeFile(nodeID, path, content, hash, 0, node_file.StatusSaved)

	if err := s.repo.CreateNextVersion(ctx, file); err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}

	s.pruneVersions(ctx, nodeID, path)

	return file, nil
}

// pruneVersions keeps one path's history bounded. Best effort on purpose:
// losing the oldest version is not worth failing the write that just produced
// the newest one. Every path that inserts a version calls this — retention that
// runs on only some of them is retention that does not bound anything.
func (s *Service) pruneVersions(ctx context.Context, nodeID uuid.UUID, path string) {
	keep := node_file.DefaultLimits().VersionsPerPath
	if s.limits != nil {
		keep = s.limits.VersionsPerPath
	}
	if err := s.repo.PruneVersions(ctx, nodeID, path, keep); err != nil {
		s.logger.Warn("failed to prune old file versions", "node_id", nodeID, "path", path, "error", err)
	}
}

// GetByID retrieves a file version by its ID
func (s *Service) GetByID(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error) {
	return s.repo.GetByID(ctx, fileID)
}

// ApplyFileVersion updates the file version status to "applying" and returns it
// This should be called before sending the command to the agent
func (s *Service) ApplyFileVersion(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error) {
	// Get the file version
	file, err := s.repo.GetByID(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}
	// Refused before the status moves, so a refused apply leaves no version
	// stuck in "applying".
	if err := node_file.ValidateWritePath(file.Path); err != nil {
		return nil, err
	}

	// Update status to "applying"
	file.Status = node_file.StatusApplying
	file.ErrorMessage = nil // Clear any previous error

	if err := s.repo.Update(ctx, file); err != nil {
		return nil, fmt.Errorf("failed to update file status: %w", err)
	}

	return file, nil
}

// UpdateStatus updates the status of a specific version
func (s *Service) UpdateStatus(ctx context.Context, nodeID uuid.UUID, path string, version int, status string, errorMsg *string) error {
	file, err := s.repo.GetByNodeIDPathAndVersion(ctx, nodeID, path, version)
	if err != nil {
		return fmt.Errorf("failed to get file: %w", err)
	}

	file.Status = status
	file.ErrorMessage = errorMsg

	if err := s.repo.Update(ctx, file); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// GetAllPathsByNodeID retrieves all unique paths for a node (latest version of each)
func (s *Service) GetAllPathsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.NodeFile, error) {
	return s.repo.GetAllPathsByNodeID(ctx, nodeID)
}

// DeleteVersions removes the whole version history of one tracked path. Used by
// untrack: the platform stops following the file, the device keeps it.
func (s *Service) DeleteVersions(ctx context.Context, nodeID uuid.UUID, path string) error {
	return s.repo.DeleteByNodeIDAndPath(ctx, nodeID, path)
}

// DeleteVersionsByNodeID removes every file version of a node. Used when the
// node itself is deleted — these rows have no foreign key to cascade from.
func (s *Service) DeleteVersionsByNodeID(ctx context.Context, nodeID uuid.UUID) error {
	return s.repo.DeleteByNodeID(ctx, nodeID)
}

// StoredBytes reports how much file content a node currently stores.
func (s *Service) StoredBytes(ctx context.Context, nodeID uuid.UUID) (int64, error) {
	return s.repo.SumContentBytesByNodeID(ctx, nodeID)
}

// ListLatestVersionMeta returns the newest version's metadata for every path a
// node has history for. One query, no content, no decryption — see the
// repository method for why the Files list must not read content at all.
func (s *Service) ListLatestVersionMeta(ctx context.Context, nodeID uuid.UUID) ([]node_file.VersionMeta, error) {
	return s.repo.ListLatestVersionMeta(ctx, nodeID)
}

// StoredBytesForAccount reports how much file content an account stores across
// every one of its nodes, for the tenant-wide ceiling.
func (s *Service) StoredBytesForAccount(ctx context.Context, accountID uuid.UUID) (int64, error) {
	return s.repo.SumContentBytesByAccountID(ctx, accountID)
}
