package image_upload

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

func validCommand() ImageUploadCommand {
	return ImageUploadCommand{
		ID:          uuid.New(),
		FileName:    "ubuntu-24.04.img",
		ContentType: "application/octet-stream",
		SizeBytes:   1024,
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ImageUploadCommand)
		wantErr string
	}{
		{name: "valid img", mutate: func(c *ImageUploadCommand) {}},
		{name: "valid iso", mutate: func(c *ImageUploadCommand) { c.FileName = "d.iso" }},
		{name: "valid xz", mutate: func(c *ImageUploadCommand) { c.FileName = "ubuntu.img.xz" }},
		{name: "valid zst", mutate: func(c *ImageUploadCommand) { c.FileName = "ubuntu.img.zst" }},
		{name: "valid uppercase ext", mutate: func(c *ImageUploadCommand) { c.FileName = "UBUNTU.IMG" }},
		{
			name:    "gz not deployable",
			mutate:  func(c *ImageUploadCommand) { c.FileName = "ubuntu.img.gz" },
			wantErr: "unsupported file type",
		},
		{
			name:    "zip not deployable",
			mutate:  func(c *ImageUploadCommand) { c.FileName = "ubuntu.zip" },
			wantErr: "unsupported file type",
		},
		{
			name:    "missing id",
			mutate:  func(c *ImageUploadCommand) { c.ID = uuid.Nil },
			wantErr: "image ID is required",
		},
		{
			name:    "empty file name",
			mutate:  func(c *ImageUploadCommand) { c.FileName = "  " },
			wantErr: "file name is required",
		},
		{
			name:    "empty file",
			mutate:  func(c *ImageUploadCommand) { c.SizeBytes = 0 },
			wantErr: "file is empty",
		},
		{
			name:    "negative size",
			mutate:  func(c *ImageUploadCommand) { c.SizeBytes = -1 },
			wantErr: "file is empty",
		},
		{
			name:    "unsupported extension",
			mutate:  func(c *ImageUploadCommand) { c.FileName = "notes.txt" },
			wantErr: "unsupported file type",
		},
		{
			name:    "no extension",
			mutate:  func(c *ImageUploadCommand) { c.FileName = "ubuntu" },
			wantErr: "unsupported file type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := validCommand()
			tt.mutate(&cmd)
			err := cmd.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
