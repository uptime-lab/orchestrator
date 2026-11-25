package grpc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// install_os refuses a plaintext image URL in production, but the
// capture upload URL — the node's WHOLE DISK, streamed outbound — had no such
// gate. Same policy, other direction: plaintext here leaks the account's data
// rather than corrupting the device.

// stubImageService overrides only what the capture dispatch branch touches;
// the embedded nil interface panics on anything else, which is what a test
// wants from an unexpected call.
type stubImageService struct {
	services.ImageService
	uploadURL string
}

func (s *stubImageService) CaptureUploadURL(context.Context, uuid.UUID, uuid.UUID, time.Duration) (string, error) {
	return s.uploadURL, nil
}

func captureServer(env, uploadURL string, allowPlaintext bool) *Server {
	cfg := &config.Config{}
	cfg.Server.Environment = env
	cfg.Image.AllowPlaintextURL = allowPlaintext
	return &Server{
		config:       cfg,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		connections:  map[string]*StreamConnection{},
		imageService: &stubImageService{uploadURL: uploadURL},
	}
}

func captureCommand() *node_command.NodeCommand {
	return &node_command.NodeCommand{
		Slug:      node_command.SlugCaptureImage,
		AccountID: uuid.New(),
		Payload:   map[string]interface{}{"image_id": uuid.New().String()},
	}
}

func TestCaptureRefusesPlaintextUploadURLInProduction(t *testing.T) {
	s := captureServer("", "http://app.example.com/capture/x?exp=1&sig=2", false)

	_, err := s.buildProtoCommandFromNodeCommand(context.Background(), captureCommand())
	if err == nil {
		t.Fatal("a plaintext capture upload URL must be refused in production")
	}
	if !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("refusal should name the transport, got: %v", err)
	}
}

func TestCaptureAcceptsHTTPSUploadURLInProduction(t *testing.T) {
	s := captureServer("", "https://app.example.com/capture/x?exp=1&sig=2", false)

	cmd, err := s.buildProtoCommandFromNodeCommand(context.Background(), captureCommand())
	if err != nil {
		t.Fatalf("an https capture upload URL must pass: %v", err)
	}
	if cmd.GetCaptureImage() == nil || cmd.GetCaptureImage().UploadUrl == "" {
		t.Fatalf("capture command not built: %+v", cmd)
	}
}

func TestCaptureAllowsPlaintextWhereInstallDoes(t *testing.T) {
	// Development and the explicit opt-in both accept
	// plaintext — one knob for both transfer directions, deliberately.
	for _, s := range []*Server{
		captureServer("development", "http://lab.local/capture/x", false),
		captureServer("", "http://lab.local/capture/x", true),
	} {
		if _, err := s.buildProtoCommandFromNodeCommand(context.Background(), captureCommand()); err != nil {
			t.Fatalf("plaintext capture URL must pass here: %v", err)
		}
	}
}
