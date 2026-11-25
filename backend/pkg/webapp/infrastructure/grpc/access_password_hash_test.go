package grpc

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// The wire carries a crypt(3) hash, never the cleartext: a node's token must
// not yield the account's SSH passwords. An unprivileged shell on one node can
// read that node's adoption token and open a gRPC stream as it — the product's
// own advertised use case is an access entry giving a contractor a non-sudo
// login.
//
// This file asserts the ABSENCE of the cleartext rather than the
// presence of a hash, because only the absence is the security property.

// The shape the agent will accept (agent-side shadowHashPattern). Duplicated
// here on purpose: the two live in different modules and a test that imported
// one from the other would pass even if they drifted apart, which is the exact
// failure — the platform emitting something the device refuses.
var shadowHash = regexp.MustCompile(`^\$6\$[A-Za-z0-9./]{1,16}\$[A-Za-z0-9./]{86}$`)

const secretPassword = "correct-horse-battery-staple"

type stubAccessResolver struct {
	cfg services.AccessConfig
}

func (s *stubAccessResolver) ResolveEffectiveAccess(context.Context, uuid.UUID, uuid.UUID) ([]services.ResolvedAccessEntry, error) {
	return nil, nil
}

func (s *stubAccessResolver) ResolveAccessConfig(context.Context, uuid.UUID, uuid.UUID, bool, bool) (services.AccessConfig, error) {
	return s.cfg, nil
}

type stubProvisioningResolver struct {
	services.OrganizationalUnitService
	eff services.EffectiveProvisioning
}

func (s *stubProvisioningResolver) ResolveEffectiveProvisioning(context.Context, uuid.UUID, uuid.UUID) (services.EffectiveProvisioning, error) {
	eff := s.eff
	eff.SSHPasswordAuthManaged = true
	eff.SSHPasswordAuth = true
	return eff, nil
}

func accessServer(cfg services.AccessConfig) *Server {
	return &Server{
		config:                    &config.Config{},
		logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		connections:               map[string]*StreamConnection{},
		accessResolver:            &stubAccessResolver{cfg: cfg},
		organizationalUnitService: &stubProvisioningResolver{},
	}
}

func accessConfigWithPassword() services.AccessConfig {
	return services.AccessConfig{
		Hash:                "deadbeef",
		ManagePasswordAuth:  true,
		PasswordAuthEnabled: true,
		Users: []services.AccessConfigUser{{
			Username:       "contractor",
			AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f user@example"},
			HasPassword:    true,
			Password:       secretPassword,
		}},
	}
}

func buildAccessCommand(t *testing.T, cfg services.AccessConfig) *Server {
	t.Helper()
	return accessServer(cfg)
}

// The decisive assertion: the cleartext must not appear ANYWHERE in the frame.
func TestAccessConfigNeverCarriesACleartextPassword(t *testing.T) {
	srv := buildAccessCommand(t, accessConfigWithPassword())

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug:      node_command.SlugApplyAccessConfig,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
	})
	if err != nil {
		t.Fatalf("building the access command failed: %v", err)
	}

	access := cmd.GetApplyAccessConfig()
	if access == nil {
		t.Fatal("no apply_access_config payload was built")
	}
	if len(access.GetUsers()) != 1 {
		t.Fatalf("expected one user, got %d", len(access.GetUsers()))
	}
	u := access.GetUsers()[0]

	// Not just "the password field is empty" — the whole marshalled message,
	// because a future field could carry it somewhere else and the leak is about
	// what reaches the device, not about one struct member.
	if strings.Contains(cmd.String(), secretPassword) {
		t.Fatal("the cleartext password appears in the command frame sent to the device")
	}
	if u.GetPassword() != "" {
		t.Errorf("the retired cleartext password field is populated: %q", u.GetPassword())
	}
	if !shadowHash.MatchString(u.GetPasswordHash()) {
		t.Errorf("password_hash is not a crypt(3) SHA-512 hash the agent will accept: %q", u.GetPasswordHash())
	}
}

// A user with no password must get neither field, and the action must say CLEAR
// — sending an empty hash with a SET action would make the agent refuse the
// whole config.
func TestAccessConfigWithoutPasswordSendsNoHash(t *testing.T) {
	cfg := accessConfigWithPassword()
	cfg.Users[0].HasPassword = false
	cfg.Users[0].Password = ""
	srv := buildAccessCommand(t, cfg)

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug:      node_command.SlugApplyAccessConfig,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
	})
	if err != nil {
		t.Fatalf("building the access command failed: %v", err)
	}
	u := cmd.GetApplyAccessConfig().GetUsers()[0]
	if u.GetPasswordHash() != "" {
		t.Errorf("a user with no password was sent a hash: %q", u.GetPasswordHash())
	}
	if u.GetPassword() != "" {
		t.Errorf("a user with no password was sent a cleartext value: %q", u.GetPassword())
	}
}

// Every apply re-hashes with a fresh salt. That must NOT disturb the config
// hash, which is what the agent's no-op check and the platform's
// applied-generation self-heal both compare — a salt that leaked into it would
// re-apply the access config to every node on every reconnect, forever.
func TestFreshSaltDoesNotChangeTheConfigHash(t *testing.T) {
	srv := buildAccessCommand(t, accessConfigWithPassword())

	first, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug: node_command.SlugApplyAccessConfig, AccountID: uuid.New(), NodeID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("building the access command failed: %v", err)
	}
	second, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug: node_command.SlugApplyAccessConfig, AccountID: uuid.New(), NodeID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("building the access command failed: %v", err)
	}

	firstHash := first.GetApplyAccessConfig().GetUsers()[0].GetPasswordHash()
	secondHash := second.GetApplyAccessConfig().GetUsers()[0].GetPasswordHash()
	if firstHash == secondHash {
		t.Error("two builds produced the same password hash; the salt is not fresh")
	}
	if first.GetApplyAccessConfig().GetConfigHash() != second.GetApplyAccessConfig().GetConfigHash() {
		t.Error("the config hash changed between builds; a fresh salt must not look like a changed desired state")
	}
}

// provision_connection is the SECOND cleartext-password sender, and its blast
// radius is wider than it looks: the password it carries is usually the OU or
// ACCOUNT default, so it is one credential shared across the fleet.
func TestProvisionConnectionNeverCarriesACleartextPassword(t *testing.T) {
	srv := accessServer(services.AccessConfig{})
	srv.organizationalUnitService = &stubProvisioningResolver{eff: services.EffectiveProvisioning{
		Username: "orchestrator",
		Password: secretPassword,
	}}

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug:      node_command.SlugProvisionConnection,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
		Payload:   map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("building provision_connection failed: %v", err)
	}
	pc := cmd.GetProvisionConnection()
	if pc == nil {
		t.Fatal("no provision_connection payload was built")
	}
	if strings.Contains(cmd.String(), secretPassword) {
		t.Fatal("the cleartext provisioning password appears in the command frame sent to the device")
	}
	if pc.GetPassword() != "" {
		t.Errorf("the retired cleartext password field is populated: %q", pc.GetPassword())
	}
	if !shadowHash.MatchString(pc.GetPasswordHash()) {
		t.Errorf("password_hash is not a crypt(3) SHA-512 hash the agent will accept: %q", pc.GetPasswordHash())
	}
}

// A key-only provisioning entry must send NO hash. An empty password and a hash
// OF an empty password are different things, and the second gives the user a
// login nobody chose.
func TestProvisionConnectionWithOnlyAKeySendsNoHash(t *testing.T) {
	srv := accessServer(services.AccessConfig{})
	srv.organizationalUnitService = &stubProvisioningResolver{eff: services.EffectiveProvisioning{
		Username:   "orchestrator",
		SSHKey:     "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f user@example",
		SSHKeyType: "ssh",
	}}

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), &node_command.NodeCommand{
		Slug:      node_command.SlugProvisionConnection,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
		Payload:   map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("building provision_connection failed: %v", err)
	}
	if got := cmd.GetProvisionConnection().GetPasswordHash(); got != "" {
		t.Errorf("a key-only provisioning entry was sent a password hash: %q", got)
	}
}
