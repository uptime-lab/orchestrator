package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

func provisionRefusal(t *testing.T, eff services.EffectiveProvisioning, payload map[string]interface{}) string {
	t.Helper()
	srv := accessServer(services.AccessConfig{})
	srv.organizationalUnitService = &stubProvisioningResolver{eff: eff}
	_, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug:      node_command.SlugProvisionConnection,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
		Payload:   payload,
	})
	if err == nil {
		t.Fatal("a provisioning config with no way to log in was accepted")
	}
	return err.Error()
}

// A dead-end provisioning config is refused (the user would be created with no
// way to log in), and the refusal is rendered verbatim in the command history.
// It has to answer the two questions the failure raises: who asked for this OS
// account, and what to set. The username that produces it is usually one nobody
// typed — a unit's provisioning default — so the origin parenthetical quotes
// the unit to go and fix (docs/COPY-STYLE.md: short, instruction-bearing, no
// slugs or internal names).
func TestProvisionConnectionDeadEndRefusalNamesTheUserAndItsOrigin(t *testing.T) {
	msg := provisionRefusal(t, services.EffectiveProvisioning{
		Username:       "orchestrator",
		UsernameSource: organizational_unit.SourceOU,
		OUName:         "Homelab",
	}, map[string]interface{}{})

	for _, want := range []string{
		`user "orchestrator"`,
		`set by its organizational unit "Homelab"`,
		"no way to log in",
		"Set a password or an SSH key",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q; an operator cannot act on it:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "provision_connection") {
		t.Errorf("the refusal leaks the command slug:\n%s", msg)
	}
}

// A username typed into the command is not inherited from anywhere, and saying
// it was would send the operator to edit a unit that carries nothing.
func TestProvisionConnectionDeadEndRefusalCreditsThePayloadUsername(t *testing.T) {
	msg := provisionRefusal(t, services.EffectiveProvisioning{
		Username:       "orchestrator",
		UsernameSource: organizational_unit.SourceOU,
		OUName:         "Homelab",
	}, map[string]interface{}{"username": "deploy"})

	if !strings.Contains(msg, `"deploy" (named in the command)`) {
		t.Errorf("the refusal does not credit the command for the username:\n%s", msg)
	}
	if strings.Contains(msg, "Homelab") {
		t.Errorf("the refusal points at a unit that did not supply the username:\n%s", msg)
	}
}

// A username inherited from the parent unit points there, not at the node's own.
func TestProvisionConnectionDeadEndRefusalNamesTheParentUnit(t *testing.T) {
	msg := provisionRefusal(t, services.EffectiveProvisioning{
		Username:       "orchestrator",
		UsernameSource: organizational_unit.SourceRootOU,
		OUName:         "Rack 3",
		RootOUName:     "Homelab",
	}, map[string]interface{}{})

	if !strings.Contains(msg, `set by its parent unit "Homelab"`) {
		t.Errorf("the refusal does not point at the parent unit:\n%s", msg)
	}
}

// A node with no username anywhere is not an error about a user. There is
// simply nothing assigned to provision, and the message says that and what to
// set, without enumerating every level or inventing an account to complain
// about.
func TestProvisionConnectionWithNothingAssignedSaysSo(t *testing.T) {
	msg := provisionRefusal(t, services.EffectiveProvisioning{}, map[string]interface{}{})

	if !strings.Contains(msg, "nothing is assigned to provision") {
		t.Errorf("the refusal does not lead with the empty state:\n%s", msg)
	}
	if strings.Contains(msg, "orchestrator") {
		t.Errorf("the refusal names a user nobody configured:\n%s", msg)
	}
	if !strings.Contains(msg, "Set a username and a password or an SSH key") {
		t.Errorf("the refusal does not say what to set:\n%s", msg)
	}
}
