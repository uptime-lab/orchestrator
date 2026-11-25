package node_command

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// A transient status is something a COMMAND does to a node, and "restarting"
// answers two different questions with one word: the pin is a claim that the
// command might take the device down, which for a bootloader flash is usually
// false. Recording which command imposed it is what lets a reader say
// "working" for the flash and keep the alarming word for a real reboot.
//
// These tests drive the release path as well, because a cause that outlives its
// status is worse than no cause: it would make an online node read as busy.

type pinRepo struct {
	repository.NodeCommandRepository
	active bool
}

func (r *pinRepo) HasActiveCommands(context.Context, uuid.UUID) (bool, error) {
	return r.active, nil
}

// pinNodeService records what the command service asked for, and which of the
// two writers it used.
type pinNodeService struct {
	services.NodeService
	status string
	slug   string
	calls  int
}

func (s *pinNodeService) UpdateStatusWithAccount(_ context.Context, _, _ uuid.UUID, status string) error {
	s.calls++
	s.status = status
	s.slug = "" // this writer clears the cause
	return nil
}

func (s *pinNodeService) UpdateStatusWithAccountPinnedBy(_ context.Context, _, _ uuid.UUID, status, slug string) error {
	s.calls++
	s.status = status
	s.slug = slug
	return nil
}

func pinFixture(active bool) (*Service, *pinNodeService) {
	recorder, _ := audittest.New()
	svc := NewService(&pinRepo{active: active}, &stubWebsocket{}, recorder, slog.Default())
	ns := &pinNodeService{}
	svc.SetNodeService(ns)
	return svc, ns
}

func TestAPinNamesTheCommandThatImposedIt(t *testing.T) {
	svc, ns := pinFixture(true)

	err := svc.updateNodeStatusForCommand(context.Background(), uuid.New(), uuid.New(),
		node_command.StatusInProgress, node_command.SlugUpdateBootOrder)
	if err != nil {
		t.Fatalf("updateNodeStatusForCommand: %v", err)
	}
	if ns.status != string(node.StatusMaintenance) {
		t.Fatalf("a running command should hold the node in maintenance, got %q", ns.status)
	}
	if ns.slug != node_command.SlugUpdateBootOrder {
		t.Fatalf("the pin does not say which command imposed it: %q", ns.slug)
	}
}

func TestTheCauseDoesNotOutliveTheStatus(t *testing.T) {
	svc, ns := pinFixture(false)

	err := svc.updateNodeStatusForCommand(context.Background(), uuid.New(), uuid.New(),
		node_command.StatusSuccess, node_command.SlugUpdateBootOrder)
	if err != nil {
		t.Fatalf("updateNodeStatusForCommand: %v", err)
	}
	if ns.status != string(node.StatusOnline) {
		t.Fatalf("the last command settled, so the node returns online, got %q", ns.status)
	}
	if ns.slug != "" {
		t.Fatalf("an online node still names a command: %q", ns.slug)
	}
}

// identify moves the node's status not at all, so it must not stamp a cause
// either — a node that is merely blinking an LED is not busy.
func TestANonDisruptiveCommandPinsNothing(t *testing.T) {
	svc, ns := pinFixture(true)

	err := svc.updateNodeStatusForCommand(context.Background(), uuid.New(), uuid.New(),
		node_command.StatusInProgress, node_command.SlugIdentify)
	if err != nil {
		t.Fatalf("updateNodeStatusForCommand: %v", err)
	}
	if ns.calls != 0 {
		t.Fatalf("identify wrote a status %d time(s): %q/%q", ns.calls, ns.status, ns.slug)
	}
}
