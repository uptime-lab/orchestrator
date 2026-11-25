package node_command

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// liveCommandRepo holds commands with their CURRENT status, so the agent-report
// path (which refuses a report on a terminal command) sees a running install.
// `answers` names the one list query that returns the commands; every other
// list query returns nothing, so each settle path is exercised alone.
type liveCommandRepo struct {
	repository.NodeCommandRepository

	cmds    map[uuid.UUID]*node_command.NodeCommand
	answers string
}

func newLiveCommandRepo(answers string, cmds ...node_command.NodeCommand) *liveCommandRepo {
	r := &liveCommandRepo{cmds: map[uuid.UUID]*node_command.NodeCommand{}, answers: answers}
	for i := range cmds {
		c := cmds[i]
		r.cmds[c.ID] = &c
	}
	return r
}

func (r *liveCommandRepo) list(query string) []node_command.NodeCommand {
	if query != r.answers {
		return nil
	}
	var out []node_command.NodeCommand
	for _, c := range r.cmds {
		if !isTerminalStatus(c.Status) {
			out = append(out, *c)
		}
	}
	return out
}

func (r *liveCommandRepo) GetByIDForUpdate(_ context.Context, id uuid.UUID) (*node_command.NodeCommand, error) {
	c, ok := r.cmds[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copied := *c
	return &copied, nil
}

func (r *liveCommandRepo) UpdateStatus(_ context.Context, id uuid.UUID, status node_command.NodeCommandStatus, _ int, _ string) error {
	r.cmds[id].Status = status
	return nil
}

func (r *liveCommandRepo) SetResult(context.Context, uuid.UUID, map[string]string) error { return nil }
func (r *liveCommandRepo) HasActiveCommands(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *liveCommandRepo) GetWorkflowExecutionNodeID(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (r *liveCommandRepo) GetStalePendingNonRebootCommandsByNodeID(context.Context, uuid.UUID, node.AgentInstanceContinuity) ([]node_command.NodeCommand, error) {
	return r.list("reconnect"), nil
}
func (r *liveCommandRepo) GetStalePendingCommandsByNodeID(context.Context, uuid.UUID) ([]node_command.NodeCommand, error) {
	return r.list("unreachable"), nil
}
func (r *liveCommandRepo) GetAbandonedInProgressCommands(context.Context, time.Time, int) ([]node_command.NodeCommand, error) {
	return r.list("connected-backstop"), nil
}
func (r *liveCommandRepo) GetUnsettledCommandsOfOfflineNodes(context.Context, time.Time, int) ([]node_command.NodeCommand, error) {
	return r.list("offline-backstop"), nil
}
func (r *liveCommandRepo) GetUnsettledCommandsByNodeID(context.Context, uuid.UUID) ([]node_command.NodeCommand, error) {
	return r.list("deleted"), nil
}

// bootRuleNodeService records forced netboots and accepts the status writes the
// settle paths make.
type bootRuleNodeService struct {
	sweepNodeService
}

func (s *bootRuleNodeService) UpdateStatusWithAccountPinnedBy(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}

// Every path that settles an install_os as failed sends the node back to
// netboot: the disk may hold a partial image, and with the command settled the
// HTTP-boot decision stops serving recovery on the install's account. A path
// that forgets it boots the half-written disk on the next power cycle, out of
// the platform's reach.
func TestEveryPathThatFailsAnInstallSendsTheNodeBackToNetboot(t *testing.T) {
	paths := []struct {
		name    string
		answers string
		status  node_command.NodeCommandStatus
		settle  func(svc *Service, install node_command.NodeCommand) error
	}{
		{"the agent reports the install failed", "", node_command.StatusInProgress,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.UpdateNodeCommandStatus(context.Background(), c.ID, "failed", 0, "digest mismatch")
			}},
		{"the agent reconnects as a different process", "reconnect", node_command.StatusInProgress,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.FailStalePendingCommandsForNode(context.Background(), c.NodeID, node.AgentInstanceChanged)
			}},
		{"the monitor gives up on an unreachable node", "unreachable", node_command.StatusInProgress,
			func(svc *Service, c node_command.NodeCommand) error {
				_, err := svc.FailCommandsForUnreachableNode(context.Background(), c.NodeID)
				return err
			}},
		{"the connected-node backstop", "connected-backstop", node_command.StatusInProgress,
			func(svc *Service, _ node_command.NodeCommand) error {
				_, err := svc.FailAbandonedInProgressCommands(context.Background())
				return err
			}},
		{"the offline-node backstop", "offline-backstop", node_command.StatusInProgress,
			func(svc *Service, _ node_command.NodeCommand) error {
				_, err := svc.FailAbandonedInProgressCommands(context.Background())
				return err
			}},
		{"a delivered command is failed by the dispatcher or a workflow", "", node_command.StatusSent,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.MarkNodeCommandFailed(context.Background(), c.ID, "workflow abandoned")
			}},
		{"redelivery gives up after its attempts", "", node_command.StatusRegistered,
			func(svc *Service, c node_command.NodeCommand) error {
				svc.retireQueuedCommand(context.Background(), c, node_command.StatusFailed, "undeliverable")
				return nil
			}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			install := cmd(node_command.SlugInstallOS, p.status)
			repo := newLiveCommandRepo(p.answers, install)
			svc, _, _ := unreachableService(&stubCommandRepo{})
			svc.repo = repo
			ns := &bootRuleNodeService{}
			svc.SetNodeService(ns)

			if err := p.settle(svc, install); err != nil {
				t.Fatalf("settling: %v", err)
			}
			if got := repo.cmds[install.ID].Status; got != node_command.StatusFailed {
				t.Fatalf("install status = %q, want failed", got)
			}
			if len(ns.forced) != 1 || ns.forced[0] != install.NodeID {
				t.Fatalf("forced netboot for %v, want exactly the install's node %s", ns.forced, install.NodeID)
			}
		})
	}
}

// The two settles that must NOT touch the boot target: a dispatch that failed
// before the send never reached the device, so its disk is intact and the
// operator's boot choice stands; and a deleted node has no row to write (its
// tombstone carries the rule).
func TestAnInstallThatNeverLeftThePlatformKeepsTheBootTarget(t *testing.T) {
	install := cmd(node_command.SlugInstallOS, node_command.StatusRegistered)
	repo := newLiveCommandRepo("", install)
	svc, _, _ := unreachableService(&stubCommandRepo{})
	svc.repo = repo
	ns := &bootRuleNodeService{}
	svc.SetNodeService(ns)

	if err := svc.MarkNodeCommandFailed(context.Background(), install.ID, "Node is not connected"); err != nil {
		t.Fatalf("marking failed: %v", err)
	}
	if got := repo.cmds[install.ID].Status; got != node_command.StatusFailed {
		t.Fatalf("install status = %q, want failed", got)
	}
	if len(ns.forced) != 0 {
		t.Fatalf("forced netboot %d times for an install that was never sent, want 0", len(ns.forced))
	}
}

// The rule lives in setStatus, and that only holds while setStatus is the one
// place this package writes a status. A sweep that calls the repository
// directly compiles, passes its own tests and silently skips the rule; this
// fails such a sweep.
func TestOnlySetStatusWritesACommandStatus(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "UpdateStatus":
					if fn.Name.Name != "setStatus" {
						t.Errorf("%s: %s writes a command status directly; route it through setStatus",
							fset.Position(call.Pos()), fn.Name.Name)
					}
				case "UpdateStatusIfCurrent":
					for _, arg := range call.Args {
						if s, ok := arg.(*ast.SelectorExpr); ok && s.Sel.Name == "StatusFailed" {
							t.Errorf("%s: %s writes failed through UpdateStatusIfCurrent, bypassing the failed-install boot rule",
								fset.Position(call.Pos()), fn.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no source files were checked")
	}
}
