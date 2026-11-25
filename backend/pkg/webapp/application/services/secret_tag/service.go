package secret_tag

import (
	"context"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
	"github.com/google/uuid"
)

// Service manages secret-tag color overrides and their rename/delete lifecycle.
// Tag membership itself stays on the secrets; this only adds presentation.
type Service struct {
	repo   repository.SecretTagRepository
	logger *slog.Logger
}

// NewService wires the secret-tag service. The repository is required.
func NewService(repo repository.SecretTagRepository, logger *slog.Logger) *Service {
	if repo == nil {
		panic("secret_tag.Service requires a repository")
	}
	return &Service{repo: repo, logger: logger.With("service", "secret_tag")}
}

// List returns the account's explicit tag color rows.
func (s *Service) List(ctx context.Context, accountID uuid.UUID) ([]secret_tag.Tag, error) {
	return s.repo.List(ctx, accountID)
}

// Upsert creates a tag with a name + color, or updates the color of an existing
// one. An empty color falls back to the deterministic default for the name.
func (s *Service) Upsert(ctx context.Context, accountID uuid.UUID, name, color string) (*secret_tag.Tag, error) {
	tag, err := secret_tag.NewTag(accountID, name, color)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Upsert(ctx, tag); err != nil {
		s.logger.ErrorContext(ctx, "failed to upsert secret tag", "error", err, "tag", tag.Name)
		return nil, err
	}
	s.logger.InfoContext(ctx, "secret tag upserted", "tag", tag.Name, "color", tag.Color)
	return tag, nil
}

// Rename cascades a tag rename across secrets, attachments and the color row.
func (s *Service) Rename(ctx context.Context, accountID uuid.UUID, oldName, newName string) error {
	from, err := secret_tag.NormalizeName(oldName)
	if err != nil {
		return err
	}
	to, err := secret_tag.NormalizeName(newName)
	if err != nil {
		return err
	}
	if err := s.repo.Rename(ctx, accountID, from, to); err != nil {
		s.logger.ErrorContext(ctx, "failed to rename secret tag", "error", err, "from", from, "to", to)
		return err
	}
	return nil
}

// Delete cascades a tag removal across secrets, attachments and the color row.
func (s *Service) Delete(ctx context.Context, accountID uuid.UUID, name string) error {
	normalized, err := secret_tag.NormalizeName(name)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, accountID, normalized); err != nil {
		s.logger.ErrorContext(ctx, "failed to delete secret tag", "error", err, "tag", normalized)
		return err
	}
	return nil
}
