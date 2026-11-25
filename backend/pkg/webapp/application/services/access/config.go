package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// ResolveAccessConfig returns the node's desired access state with SSH key
// content hydrated, ready to be mapped onto the wire.
//
// Errors are propagated, never swallowed into an empty config: an empty config
// tells the agent to lock every managed user, so a transient database failure
// must fail the command rather than silently revoke a fleet's access.
func (s *Service) ResolveAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID, managePasswordAuth, passwordAuthEnabled bool) (services.AccessConfig, error) {
	entries, err := s.ResolveEffectiveAccess(ctx, accountID, nodeID)
	if err != nil {
		return services.AccessConfig{}, fmt.Errorf("failed to resolve effective access: %w", err)
	}

	// Hydrate every referenced key in one round trip rather than per entry.
	keyIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for i := range entries {
		for _, id := range entries[i].KeyIDs {
			if !seen[id] {
				seen[id] = true
				keyIDs = append(keyIDs, id)
			}
		}
	}
	keysByID := map[uuid.UUID]ssh_key.SSHKey{}
	if len(keyIDs) > 0 {
		keys, err := s.sshKeyRepo.GetByIDs(ctx, accountID, keyIDs)
		if err != nil {
			return services.AccessConfig{}, fmt.Errorf("failed to load referenced ssh keys: %w", err)
		}
		for _, k := range keys {
			keysByID[k.ID] = k
		}
		// A referenced key that no longer exists is dropped from the set with a
		// warning, NOT a hard error. A vanished key genuinely should not grant
		// access, so dropping it is correct; hard-failing here would instead
		// freeze every access-config resolve for the node forever if a cascade
		// ever left a dangling reference (it should not — see the delete order in
		// ssh_key/service.go — but the resolver must not be one transient bug
		// away from permanently un-revocable nodes).
		if len(keysByID) != len(keyIDs) {
			s.logger.WarnContext(ctx, "access entries reference ssh keys that no longer exist; dropping them",
				"referenced", len(keyIDs), "found", len(keysByID), "account_id", accountID, "node_id", nodeID)
		}
	}

	cfg := services.AccessConfig{
		ManagePasswordAuth:  managePasswordAuth,
		PasswordAuthEnabled: passwordAuthEnabled,
	}
	for i := range entries {
		e := &entries[i]
		switch e.Type {
		case string(access_entry.TypeUser):
			user := services.AccessConfigUser{
				Username:       e.Username,
				Sudo:           e.Sudo,
				SudoNoPasswd:   e.SudoNoPasswd,
				HasPassword:    e.HasPassword,
				Password:       e.Password,
				Locked:         e.Lifecycle == string(access_entry.LifecycleLocked),
				AdoptUnmanaged: e.AdoptUnmanaged,
				Principals:     append([]string(nil), e.Principals...),
			}
			for _, id := range e.KeyIDs {
				if k, ok := keysByID[id]; ok {
					if line := strings.TrimSpace(k.KeyContent); line != "" {
						user.AuthorizedKeys = append(user.AuthorizedKeys, line)
					}
				}
			}
			cfg.Users = append(cfg.Users, user)
		case string(access_entry.TypeCA):
			for _, id := range e.KeyIDs {
				k, ok := keysByID[id]
				if !ok {
					continue
				}
				// A CA is a pure trust anchor; certificate principals live on
				// user entries (AccessConfigUser.Principals), so none are emitted
				// here. The agent still folds any legacy on-device CA principals.
				cfg.CAs = append(cfg.CAs, services.AccessConfigCA{
					PublicKey: strings.TrimSpace(k.KeyContent),
				})
			}
		}
	}

	cfg.Hash = hashAccessConfig(cfg)
	return cfg, nil
}

// hashAccessConfig fingerprints the desired state so a repeat delivery of the
// same configuration is a cheap no-op on the node. Built from a canonical,
// order-independent rendering: reordering entries or keys is not a change.
//
// The password VALUE is deliberately excluded and only its presence is mixed in
// — the hash travels to the node, is echoed back in command results and is
// written into the sshd drop-in header, so it must not be a brute-forceable
// commitment to a credential. The cost is that a password-only rotation
// produces the same hash; the agent therefore always applies password actions
// rather than trusting the no-op check for them.
//
// Everything else the agent would act on MUST be covered here, including the
// password-auth policy and the adoption flag. A change the hash cannot see is a
// change the agent skips while reporting success.
func hashAccessConfig(cfg services.AccessConfig) string {
	h := sha256.New()

	users := make([]string, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		keys := append([]string(nil), u.AuthorizedKeys...)
		sort.Strings(keys)
		principals := append([]string(nil), u.Principals...)
		sort.Strings(principals)
		users = append(users, fmt.Sprintf("u|%s|%t|%t|%t|%t|%t|%s|%s",
			u.Username, u.Sudo, u.SudoNoPasswd, u.HasPassword, u.Locked, u.AdoptUnmanaged,
			strings.Join(keys, "\x1f"), strings.Join(principals, "\x1f")))
	}
	sort.Strings(users)

	cas := make([]string, 0, len(cfg.CAs))
	for _, c := range cfg.CAs {
		principals := append([]string(nil), c.Principals...)
		sort.Strings(principals)
		cas = append(cas, fmt.Sprintf("c|%s|%s", c.PublicKey, strings.Join(principals, "\x1f")))
	}
	sort.Strings(cas)

	// The password-auth policy is part of the desired state: leaving it out let
	// an operator flip "keys only" on a converged node and have the agent report
	// success without rewriting sshd, so the policy silently did not apply.
	h.Write([]byte(fmt.Sprintf("p|%t|%t\x1e", cfg.ManagePasswordAuth, cfg.PasswordAuthEnabled)))
	for _, line := range users {
		h.Write([]byte(line))
		h.Write([]byte("\x1e"))
	}
	for _, line := range cas {
		h.Write([]byte(line))
		h.Write([]byte("\x1e"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
