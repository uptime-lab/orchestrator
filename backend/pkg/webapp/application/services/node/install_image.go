package node

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// checkInstallImage refuses, before a command row exists, an install_os whose
// image the platform already knows it cannot hand over.
//
// The same checks run again when the command is built for the device, and
// that build stays the authority. But a failure there arrives AFTER the
// dispatch answered success, as a command that fails a moment later — and an
// image still being compressed passed that build entirely: it has a file and a
// checksum, so the device was sent a half-written object and wiped its disk
// before the download failed. Refusing here is what makes the answer to the
// request the truth about it.
//
// Only what is certain is refused. A lookup that fails for any reason other
// than "no such image in this account" defers to the build, as it always has:
// a database blip must not make the fleet un-installable.
func (s *Service) checkInstallImage(ctx context.Context, accountID uuid.UUID, target *node.Node, params map[string]interface{}) error {
	raw, present := params["image_id"]
	idStr, isString := raw.(string)
	if present && raw != nil && !isString {
		return app_errors.OperatorRefusal(errors.New("install_os: image_id must be a string"))
	}
	idStr = strings.TrimSpace(idStr)

	if idStr == "" {
		// The image may come from the node's provisioning policy, which the
		// build resolves. Resolve it here too when the resolver is wired, so a
		// command with no image anywhere is refused rather than accepted.
		if s.accessGeneration == nil || s.accessGeneration.ou == nil {
			return nil
		}
		eff, err := s.accessGeneration.ou.ResolveEffectiveProvisioning(ctx, accountID, target.ID)
		if err != nil {
			s.logger.WarnContext(ctx, "could not resolve the node's provisioning policy for install_os; deferring the image check to dispatch",
				"node_id", target.ID, "error", err)
			return nil
		}
		if eff.OSImageID == nil {
			return app_errors.OperatorRefusal(errors.New(
				"install_os: no image_id was given and the node's provisioning policy names no OS image"))
		}
		idStr = eff.OSImageID.String()
	}

	imageID, err := uuid.Parse(idStr)
	if err != nil {
		return app_errors.OperatorRefusal(errors.New("install_os: image_id is not a valid image id"))
	}
	if s.imageService == nil {
		return nil
	}
	img, err := s.imageService.GetByID(ctx, image_get.ImageGetQuery{
		ID:          imageID,
		AuthContext: commands.NewAuthContext(accountID, uuid.Nil),
	})
	if errors.Is(err, app_errors.ErrImageNotFound) {
		// The same answer for an image that does not exist and one that
		// belongs to another account: the lookup is account-scoped, and the
		// refusal must not tell the two apart.
		return app_errors.OperatorRefusal(fmt.Errorf("install_os: %w", app_errors.ErrImageNotFound))
	}
	if err != nil {
		s.logger.WarnContext(ctx, "could not read the install_os image; deferring the image check to dispatch",
			"node_id", target.ID, "image_id", imageID, "error", err)
		return nil
	}

	// A busy record is not installable whatever else it carries: a compression
	// or an upload replaces its file and checksum while it runs, so the object
	// the device would download is not the one the checksum describes.
	if image.IsBusy(img.CompressionStatus) {
		return app_errors.OperatorRefusal(fmt.Errorf(
			"install_os: image %q is still being processed (%s); wait until it is ready before installing it",
			img.Title, img.CompressionStatus))
	}
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
		return app_errors.OperatorRefusal(fmt.Errorf("install_os: image %q has no uploaded file", img.Title))
	}
	if img.Checksum == "" {
		return app_errors.OperatorRefusal(fmt.Errorf("install_os: image %q has no checksum yet", img.Title))
	}

	// A backup of another node must be generalized during install, which an
	// agent below the floor cannot do; refusing beats cloning an identity.
	if img.NeedsIdentityStripFor(target.ID) &&
		!node_command.MeetsMinAgentVersion(target.AgentVersion, node_command.MinAgentVersionStripIdentity) {
		return &AgentTooOldError{
			NodeID:  target.ID,
			Slug:    node_command.SlugInstallOS,
			Have:    target.AgentVersion,
			Require: node_command.MinAgentVersionStripIdentity,
		}
	}
	return nil
}
