package access_entry

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

func strptr(s string) *string { return &s }

func TestValidate_Target(t *testing.T) {
	acc := uuid.New()
	ou := uuid.New()
	node := uuid.New()

	// Both targets set is invalid.
	e := &AccessEntry{AccountID: acc, Type: TypeUser, Username: "alice", OUIDs: []uuid.UUID{ou}, NodeID: &node}
	if err := e.Validate(); err != ErrInvalidTarget {
		t.Fatalf("expected ErrInvalidTarget, got %v", err)
	}

	// Neither target set is valid. Without AccountWide it is an unassigned
	// entry (applies to no node); with AccountWide it is the account default.
	e = &AccessEntry{AccountID: acc, Type: TypeUser, Username: "alice"}
	if err := e.Validate(); err != nil {
		t.Fatalf("unassigned entry should be valid, got %v", err)
	}
	if e.TargetScope() != "unassigned" {
		t.Fatalf("expected unassigned scope, got %s", e.TargetScope())
	}
	e.AccountWide = true
	if e.TargetScope() != "account" {
		t.Fatalf("expected account scope, got %s", e.TargetScope())
	}
}

func TestValidate_Username(t *testing.T) {
	acc := uuid.New()
	cases := []struct {
		name string
		user string
		want error
	}{
		{"empty", "", ErrUsernameRequired},
		{"root reserved", "root", ErrUsernameReserved},
		{"uppercase invalid", "Alice", ErrUsernameInvalid},
		{"leading digit invalid", "1ivan", ErrUsernameInvalid},
		{"valid", "alice", nil},
		{"valid underscore", "_svc-ci", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &AccessEntry{AccountID: acc, Type: TypeUser, Username: c.user}
			if err := e.Validate(); err != c.want {
				t.Fatalf("username %q: expected %v, got %v", c.user, c.want, err)
			}
		})
	}
}

func TestValidate_CAPrincipals(t *testing.T) {
	acc := uuid.New()
	// A CA entry is a pure trust anchor: principals live on USER entries (the
	// AuthorizedPrincipalsFile model), so a CA without any is valid.
	e := &AccessEntry{AccountID: acc, Type: TypeCA, KeyIDs: []uuid.UUID{uuid.New()}}
	if err := e.Validate(); err != nil {
		t.Fatalf("CA without principals should be valid, got %v", err)
	}
	// Legacy principals on the trust are still carried without complaint.
	e.Principals = []string{"  ", "deploy"}
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateKeyConformance(t *testing.T) {
	acc := uuid.New()
	sshKey := uuid.New()
	caKey := uuid.New()
	keyTypes := map[uuid.UUID]ssh_key.KeyType{
		sshKey: ssh_key.KeyTypeSSH,
		caKey:  ssh_key.KeyTypeCA,
	}

	// User entry with an ssh key: ok.
	u := &AccessEntry{AccountID: acc, Type: TypeUser, Username: "alice", KeyIDs: []uuid.UUID{sshKey}}
	if err := u.ValidateKeyConformance(keyTypes); err != nil {
		t.Fatalf("user+ssh key should conform, got %v", err)
	}
	// User entry with a ca key: mismatch.
	u.KeyIDs = []uuid.UUID{caKey}
	if err := u.ValidateKeyConformance(keyTypes); err != ErrKeyTypeMismatch {
		t.Fatalf("expected ErrKeyTypeMismatch, got %v", err)
	}
	// Unknown key.
	u.KeyIDs = []uuid.UUID{uuid.New()}
	if err := u.ValidateKeyConformance(keyTypes); err != ErrUnknownKey {
		t.Fatalf("expected ErrUnknownKey, got %v", err)
	}
	// CA entry with exactly one ca key: ok.
	ca := &AccessEntry{AccountID: acc, Type: TypeCA, Principals: []string{"deploy"}, KeyIDs: []uuid.UUID{caKey}}
	if err := ca.ValidateKeyConformance(keyTypes); err != nil {
		t.Fatalf("ca+ca key should conform, got %v", err)
	}
	// CA entry with two keys: rejected.
	ca.KeyIDs = []uuid.UUID{caKey, caKey}
	if err := ca.ValidateKeyConformance(keyTypes); err != ErrCAKeyRequired {
		t.Fatalf("expected ErrCAKeyRequired, got %v", err)
	}
}

func TestDesiredState(t *testing.T) {
	acc := uuid.New()

	// No keys and no password -> locked (no way in at all).
	e := &AccessEntry{AccountID: acc, Type: TypeUser, Username: "alice"}
	if ds := e.DesiredState(); ds.Lifecycle != LifecycleLocked {
		t.Fatalf("no keys and no password should be locked, got %s", ds.Lifecycle)
	}

	// A password but no keys -> active: password login is a valid way in.
	e.Password = strptr("s3cret")
	if ds := e.DesiredState(); ds.Lifecycle != LifecycleActive {
		t.Fatalf("password-only user should be active, got %s", ds.Lifecycle)
	}
	e.Password = nil

	// Non-empty key set -> active.
	e.KeyIDs = []uuid.UUID{uuid.New()}
	if ds := e.DesiredState(); ds.Lifecycle != LifecycleActive {
		t.Fatalf("non-empty set should be active, got %s", ds.Lifecycle)
	}

	// Sudo without password -> NOPASSWD.
	e.Sudo = true
	ds := e.DesiredState()
	if !ds.SudoEnabled || !ds.SudoNoPasswd || ds.HasPassword {
		t.Fatalf("sudo without password should be NOPASSWD, got %+v", ds)
	}

	// Sudo with password -> password-gated.
	e.Password = strptr("s3cret")
	ds = e.DesiredState()
	if !ds.SudoEnabled || ds.SudoNoPasswd || !ds.HasPassword {
		t.Fatalf("sudo with password should require password, got %+v", ds)
	}
}

func TestValidatePrincipals(t *testing.T) {
	cases := []struct {
		name       string
		principals []string
		wantErr    bool
	}{
		{"empty is fine", nil, false},
		{"plain names", []string{"deploy", "ci-bot", "user.name", "a_b", "svc@host"}, false},
		{"a space is rejected", []string{"alice bob"}, true},
		{"a newline is rejected", []string{"ops\nroot"}, true},
		{"a slash is rejected", []string{"team/lead"}, true},
		{"one bad among good is rejected", []string{"ok", "not ok"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &AccessEntry{Type: TypeUser, Principals: c.principals}
			err := e.ValidatePrincipals()
			if c.wantErr && err == nil {
				t.Errorf("principals %q: expected an error, got nil", c.principals)
			}
			if !c.wantErr && err != nil {
				t.Errorf("principals %q: expected nil, got %v", c.principals, err)
			}
		})
	}
}
