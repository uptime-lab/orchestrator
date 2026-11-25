package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// provision_connection creates one OS user from the node's provisioning
// policy. A node whose policy names no user, or none anyone could log into,
// was answered "dispatched" and then failed when the command was built.

type provisioningResolver struct {
	services.OrganizationalUnitService
	eff services.EffectiveProvisioning
	err error
}

func (r *provisioningResolver) ResolveEffectiveProvisioning(context.Context, uuid.UUID, uuid.UUID) (services.EffectiveProvisioning, error) {
	return r.eff, r.err
}

func provisionService(eff services.EffectiveProvisioning, err error) (*Service, *clampCapturingCommandService, uuid.UUID) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	n.AgentVersion = "0.8.0"
	s.accessGeneration = &accessGenerationDeps{ou: &provisioningResolver{eff: eff, err: err}}
	return s, capture, n.ID
}

func TestProvisionConnectionWithNothingToCreateIsRefusedAtDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		eff     services.EffectiveProvisioning
		mention string
	}{
		{"no username anywhere", services.EffectiveProvisioning{Password: "x"}, "no OS username"},
		{"a user nobody can log into", services.EffectiveProvisioning{Username: "pi"}, "no password and no SSH key"},
		{"an undecryptable password", services.EffectiveProvisioning{Username: "pi", PasswordUnreadable: true}, "cannot be decrypted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, capture, _ := provisionService(tc.eff, nil)
			_, err := dispatch(t, s, nodeOf(s), node_command.SlugProvisionConnection, nil)
			var incomplete *ProvisioningIncompleteError
			if !errors.As(err, &incomplete) || incomplete.HTTPStatus() != 409 || incomplete.ErrorCode() != "provisioning_incomplete" {
				t.Fatalf("err = %v, want a 409 ProvisioningIncompleteError", err)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("the refusal does not name what is missing (%q): %s", tc.mention, err)
			}
			if capture.slug != "" {
				t.Error("a command was registered")
			}
		})
	}
}

// A correctly configured node dispatches exactly as before, and a resolver
// that cannot answer defers to the build rather than blocking the node.
func TestProvisionConnectionThatCanRunStillDispatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		eff  services.EffectiveProvisioning
		err  error
	}{
		{"user with a password", services.EffectiveProvisioning{Username: "pi", Password: "x"}, nil},
		{"user with a key", services.EffectiveProvisioning{Username: "pi", SSHKey: "ssh-ed25519 AAAA"}, nil},
		{"resolver failure", services.EffectiveProvisioning{}, errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, capture, _ := provisionService(tc.eff, tc.err)
			_, err := dispatch(t, s, nodeOf(s), node_command.SlugProvisionConnection, map[string]interface{}{"target_device": "/dev/nvme0n1"})
			if !errors.Is(err, errStopAfterCapture) {
				t.Fatalf("err = %v, want the dispatch to reach registration", err)
			}
			if capture.payload["target_device"] != "/dev/nvme0n1" || capture.payload["root_partition"] != 0 {
				t.Errorf("payload = %v, want target_device and root_partition only", capture.payload)
			}
			for _, k := range provisionConnectionPolicyParams {
				if _, present := capture.payload[k]; present {
					t.Errorf("payload carries %s", k)
				}
			}
		})
	}
}

// The command takes no credential: one named in the request was dropped and
// the command ran with the policy's instead, reported as a success.
func TestProvisionConnectionRefusesACredentialInTheRequest(t *testing.T) {
	for _, name := range provisionConnectionPolicyParams {
		s, capture, _ := provisionService(services.EffectiveProvisioning{Username: "pi", Password: "x"}, nil)
		_, err := dispatch(t, s, nodeOf(s), node_command.SlugProvisionConnection, map[string]interface{}{name: "value"})
		if !isOperatorRefusal(err) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want an operator refusal naming it", name, err)
		}
		if capture.slug != "" {
			t.Errorf("%s: a command was registered", name)
		}
	}
}

func nodeOf(s *Service) *node.Node { return s.nodeRepo.(*payloadBoundRepo).n }
