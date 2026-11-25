package access

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

func TestHashAccessConfigIgnoresOrdering(t *testing.T) {
	a := services.AccessConfig{
		Users: []services.AccessConfigUser{
			{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAA", "ssh-ed25519 BBB"}, Sudo: true},
			{Username: "bob", AuthorizedKeys: []string{"ssh-ed25519 CCC"}},
		},
		CAs: []services.AccessConfigCA{
			{PublicKey: "ssh-rsa CA1", Principals: []string{"ops", "sre"}},
		},
	}
	b := services.AccessConfig{
		// Same content, every list in a different order.
		Users: []services.AccessConfigUser{
			{Username: "bob", AuthorizedKeys: []string{"ssh-ed25519 CCC"}},
			{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 BBB", "ssh-ed25519 AAA"}, Sudo: true},
		},
		CAs: []services.AccessConfigCA{
			{PublicKey: "ssh-rsa CA1", Principals: []string{"sre", "ops"}},
		},
	}

	if hashAccessConfig(a) != hashAccessConfig(b) {
		t.Fatal("reordering entries or keys changed the config hash; the agent would re-apply an unchanged config")
	}
}

func TestHashAccessConfigDetectsRealChanges(t *testing.T) {
	base := services.AccessConfig{
		Users: []services.AccessConfigUser{
			{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAA"}, Sudo: true},
		},
	}
	baseHash := hashAccessConfig(base)

	cases := []struct {
		name   string
		mutate func(*services.AccessConfig)
	}{
		{"key rotated", func(c *services.AccessConfig) {
			c.Users[0].AuthorizedKeys = []string{"ssh-ed25519 ZZZ"}
		}},
		{"key removed - user becomes locked", func(c *services.AccessConfig) {
			c.Users[0].AuthorizedKeys = nil
			c.Users[0].Locked = true
		}},
		{"key added", func(c *services.AccessConfig) {
			c.Users[0].AuthorizedKeys = append(c.Users[0].AuthorizedKeys, "ssh-ed25519 BBB")
		}},
		{"sudo revoked", func(c *services.AccessConfig) {
			c.Users[0].Sudo = false
		}},
		{"nopasswd sudo toggled", func(c *services.AccessConfig) {
			c.Users[0].SudoNoPasswd = true
		}},
		{"password added", func(c *services.AccessConfig) {
			c.Users[0].HasPassword = true
		}},
		{"user added", func(c *services.AccessConfig) {
			c.Users = append(c.Users, services.AccessConfigUser{Username: "bob"})
		}},
		{"CA trusted", func(c *services.AccessConfig) {
			c.CAs = append(c.CAs, services.AccessConfigCA{PublicKey: "ssh-rsa CA1", Principals: []string{"ops"}})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := services.AccessConfig{
				Users: []services.AccessConfigUser{
					{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAA"}, Sudo: true},
				},
			}
			tc.mutate(&mutated)
			if hashAccessConfig(mutated) == baseHash {
				t.Fatalf("%s did not change the config hash; the agent would skip applying it as a no-op", tc.name)
			}
		})
	}
}

// The hash travels to the node and comes back in command results, so it must
// not be a brute-forceable commitment to a password.
func TestHashAccessConfigExcludesPasswordValue(t *testing.T) {
	withPassword := func(pw string) services.AccessConfig {
		return services.AccessConfig{
			Users: []services.AccessConfigUser{
				{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAA"}, HasPassword: true, Password: pw},
			},
		}
	}
	if hashAccessConfig(withPassword("hunter2")) != hashAccessConfig(withPassword("correct horse")) {
		t.Fatal("config hash varies with the password value; it must commit only to presence")
	}
	if strings.Contains(hashAccessConfig(withPassword("hunter2")), "hunter2") {
		t.Fatal("password leaked into the config hash")
	}
}

// The policy is what an operator flips when hardening a fleet, and on a
// key-only fleet no password action forces a re-apply. If the hash cannot see
// the flag, the agent short-circuits and reports success while sshd is never
// rewritten — the policy silently does not apply.
func TestHashAccessConfigCoversPasswordAuthPolicy(t *testing.T) {
	base := services.AccessConfig{
		Users: []services.AccessConfigUser{
			{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAA"}},
		},
		ManagePasswordAuth:  true,
		PasswordAuthEnabled: true,
	}

	off := base
	off.PasswordAuthEnabled = false
	if hashAccessConfig(base) == hashAccessConfig(off) {
		t.Fatal("turning password auth off did not change the hash; the agent would skip it as a no-op")
	}

	unmanaged := base
	unmanaged.ManagePasswordAuth = false
	if hashAccessConfig(base) == hashAccessConfig(unmanaged) {
		t.Fatal("ceasing to manage password auth did not change the hash")
	}
}

// adopt_unmanaged decides whether the agent takes over a pre-existing account.
// Flipping it changes what the reconcile does, so it has to change the hash.
func TestHashAccessConfigCoversAdoptFlag(t *testing.T) {
	base := services.AccessConfig{
		Users: []services.AccessConfigUser{{Username: "ubuntu", AuthorizedKeys: []string{"ssh-ed25519 AAA"}}},
	}
	adopting := services.AccessConfig{
		Users: []services.AccessConfigUser{{Username: "ubuntu", AuthorizedKeys: []string{"ssh-ed25519 AAA"}, AdoptUnmanaged: true}},
	}
	if hashAccessConfig(base) == hashAccessConfig(adopting) {
		t.Fatal("toggling adopt_unmanaged did not change the hash")
	}
}
