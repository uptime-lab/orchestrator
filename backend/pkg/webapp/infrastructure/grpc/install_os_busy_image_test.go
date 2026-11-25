package grpc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// busyImageService serves one image record; anything past the lookup (the
// signed URL) panics through the nil embed, which is the assertion that a busy
// image never gets that far.
type busyImageService struct {
	services.ImageService
	img *image.Image
}

func (s *busyImageService) GetByID(context.Context, image_get.ImageGetQuery) (*image.Image, error) {
	return s.img, nil
}

// An image a compression or upload holds has a file and a checksum that
// describe different objects. The build is the last point before the device
// wipes its disk, and the job may have started after the dispatch check.
func TestTheInstallOSBuildRefusesABusyImage(t *testing.T) {
	for _, status := range image.BusyStatuses() {
		t.Run(status, func(t *testing.T) {
			s := &Server{
				config:      &config.Config{},
				logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				connections: map[string]*StreamConnection{},
				imageService: &busyImageService{img: &image.Image{
					ID:                uuid.New(),
					Title:             "ubuntu",
					FileStorageInfo:   &image.FileStorageInfo{FilePath: "images/ubuntu.img"},
					Checksum:          "abc",
					CompressionStatus: status,
				}},
			}
			cmd := &node_command.NodeCommand{
				Slug:      node_command.SlugInstallOS,
				AccountID: uuid.New(),
				NodeID:    uuid.New(),
				Payload:   map[string]interface{}{"image_id": uuid.New().String(), "target_device": "/dev/nvme0n1"},
			}
			_, err := s.buildProtoCommandFromNodeCommand(context.Background(), cmd)
			if err == nil || !strings.Contains(err.Error(), "still being processed") {
				t.Fatalf("err = %v, want the busy-image refusal", err)
			}
		})
	}
}
