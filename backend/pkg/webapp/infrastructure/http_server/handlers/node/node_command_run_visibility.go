package node

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// runCommandFilter is the run view's omission of commands whose presence
// answers a secret-fed condition (workflow_execution.Service).
type runCommandFilter interface {
	WithoutHiddenRunCommands(ctx context.Context, accountID uuid.UUID, commands []node_command.NodeCommand) ([]node_command.NodeCommand, error)
}

// SetRunCommandFilter wires the omission the command reads apply for a
// caller without secret.read_value. A node's command list and a single
// command are a second read path to a run's commands, so they may not show
// one the run's own view withholds from the same member.
func (h *NodeHandler) SetRunCommandFilter(f runCommandFilter) {
	h.runCommands = f
}

var errRunCommandFilterUnwired = errors.New("run command visibility is not wired; refusing to list run commands unfiltered")

// visibleRunCommands drops the commands a caller without secret.read_value
// may not see. Unwired, a list holding any run's command is refused rather
// than served whole.
func (h *NodeHandler) visibleRunCommands(ctx context.Context, accountID uuid.UUID, commands []node_command.NodeCommand) ([]node_command.NodeCommand, error) {
	if h.runCommands != nil {
		return h.runCommands.WithoutHiddenRunCommands(ctx, accountID, commands)
	}
	for i := range commands {
		if commands[i].WorkflowExecutionNodeID != nil {
			return nil, errRunCommandFilterUnwired
		}
	}
	return commands, nil
}

// runCommandVisible is visibleRunCommands for one command.
func (h *NodeHandler) runCommandVisible(ctx context.Context, accountID uuid.UUID, cmd *node_command.NodeCommand) (bool, error) {
	visible, err := h.visibleRunCommands(ctx, accountID, []node_command.NodeCommand{*cmd})
	return len(visible) == 1, err
}
