package workflow_template

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func step(order int, slug string, payload map[string]interface{}) WorkflowStep {
	return WorkflowStep{
		Order:   order,
		Command: CommandSpec{Slug: slug, Payload: payload},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		tpl     *WorkflowTemplate
		wantErr string
	}{
		{
			"valid multi-step template",
			NewWorkflowTemplate(uuid.New(), "weekly", "", false, []WorkflowStep{
				step(0, "install_os", map[string]interface{}{"target_device": "/dev/nvme0n1", "reboot": true}),
				step(1, "update_file", map[string]interface{}{"path": "/etc/x", "content": "hi"}),
			}),
			"",
		},
		{
			"name required",
			NewWorkflowTemplate(uuid.New(), "", "", false, []WorkflowStep{step(0, "reboot", nil)}),
			"name is required",
		},
		{
			"at least one step",
			NewWorkflowTemplate(uuid.New(), "x", "", false, nil),
			"at least one step",
		},
		{
			"unknown slug rejected",
			NewWorkflowTemplate(uuid.New(), "x", "", false, []WorkflowStep{step(0, "format_c", nil)}),
			"invalid command.slug",
		},
		{
			"previously missing slugs now allowed",
			NewWorkflowTemplate(uuid.New(), "x", "", false, []WorkflowStep{
				step(0, "update_agent", nil),
				step(1, "set_hostname", map[string]interface{}{"hostname": "node-01"}),
			}),
			"",
		},
		{
			"payload validated at save time",
			NewWorkflowTemplate(uuid.New(), "x", "", false, []WorkflowStep{
				step(0, "erase_drive", nil),
			}),
			"'target_device' is required",
		},
		{
			"duplicate order rejected",
			NewWorkflowTemplate(uuid.New(), "x", "", false, []WorkflowStep{
				step(0, "reboot", nil),
				step(0, "shutdown", nil),
			}),
			"duplicate order",
		},
		{
			"negative order rejected",
			NewWorkflowTemplate(uuid.New(), "x", "", false, []WorkflowStep{
				step(-1, "reboot", nil),
			}),
			"non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tpl.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
