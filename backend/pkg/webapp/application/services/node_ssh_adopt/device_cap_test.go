package node_ssh_adopt

import (
	"context"
	"errors"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// capRefusingProvisioner is the node service at its device cap.
type capRefusingProvisioner struct {
	fakeProvisioner
	checks int
}

func (p *capRefusingProvisioner) CheckDeviceCap(context.Context, audit.Actor) error {
	p.checks++
	return &entitlement.DeviceCapError{Cap: 25, Count: 25}
}

// An adoption refused at the device cap is refused BEFORE a job exists and
// before the device is dialled: a job that installed the agent, wrote a
// token and only then learned the row cannot be created would leave a
// half-adopted machine. The refusal keeps its type, so the HTTP layer
// answers it as the 409 every other admission path answers.
func TestStartAdoptionRefusedAtTheDeviceCapBeforeTheJob(t *testing.T) {
	h := newHarness(t, nil)
	prov := &capRefusingProvisioner{}
	h.svc.nodeProvisioner = prov

	job, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())

	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v, want *entitlement.DeviceCapError", err)
	}
	if job != nil {
		t.Errorf("a refused adoption returned a job: %+v", job)
	}
	if h.jobs.count() != 0 {
		t.Errorf("%d job(s) were created for a refused adoption", h.jobs.count())
	}
	if dials := h.ssh.Dials(); len(dials) != 0 {
		t.Errorf("the device was dialled %d time(s) before the cap was asked", len(dials))
	}
	if prov.checks != 1 {
		t.Errorf("the cap was asked %d time(s), want once", prov.checks)
	}
}

// uncheckableCapProvisioner is the node service unable to check the cap.
type uncheckableCapProvisioner struct {
	fakeProvisioner
}

func (uncheckableCapProvisioner) CheckDeviceCap(context.Context, audit.Actor) error {
	return &entitlement.DeviceCapUnverifiedError{Cap: 25}
}

// A pre-check that cannot count refuses the adoption the same way, before a
// job or a dial: the job's own create would meet the same uncheckable cap
// after the agent was installed. The refusal keeps its type, so the HTTP
// layer answers the typed 503.
func TestStartAdoptionRefusedWhenTheDeviceCapCannotBeChecked(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.nodeProvisioner = uncheckableCapProvisioner{}

	job, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())

	if !errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		t.Fatalf("err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	if job != nil || h.jobs.count() != 0 {
		t.Errorf("a refused adoption created a job: %+v (%d stored)", job, h.jobs.count())
	}
	if dials := h.ssh.Dials(); len(dials) != 0 {
		t.Errorf("the device was dialled %d time(s)", len(dials))
	}
}
