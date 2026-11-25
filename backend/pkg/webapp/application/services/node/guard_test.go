package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// guardFakeRepo stubs only what guardSerialBinding touches; any other repo
// call panics via the embedded nil interface, proving the guard queries
// nothing else.
type guardFakeRepo struct {
	repository.NodeRepository
	owners []*node.Node
	err    error
	calls  int
	// holder answers the low-word lookup; lowWordAsked records each serial
	// it was asked about.
	holder       *repository.SerialHolder
	lowWordErr   error
	lowWordAsked []string
}

func (f *guardFakeRepo) ListByExactSerial(_ context.Context, _ string) ([]*node.Node, error) {
	f.calls++
	return f.owners, f.err
}

func (f *guardFakeRepo) LowWordHolderInAnotherAccount(_ context.Context, _ uuid.UUID, serial string) (*repository.SerialHolder, error) {
	f.lowWordAsked = append(f.lowWordAsked, serial)
	return f.holder, f.lowWordErr
}

// lowWordHolderAmong is LowWordHolderInAnotherAccount over an in-memory row
// set, for the fakes that model several accounts.
func lowWordHolderAmong(nodes []*node.Node, accountID uuid.UUID, serial string) *repository.SerialHolder {
	low := node.LowWord(serial)
	for _, n := range nodes {
		if n.AccountID == accountID || n.Fingerprint == nil {
			continue
		}
		stored := node.NormalizeSerial(n.Fingerprint.HardwareSerial)
		if stored == serial {
			continue
		}
		storedLow := node.LowWord(stored)
		switch {
		case low != "" && storedLow == low,
			low != "" && storedLow == "" && len(stored) >= node.LowWordLen && strings.HasSuffix(serial, stored),
			low == "" && storedLow != "" && len(serial) >= node.LowWordLen && strings.HasSuffix(stored, serial):
			return &repository.SerialHolder{AccountID: n.AccountID, NodeID: n.ID}
		}
	}
	return nil
}

func guardService(repo repository.NodeRepository) *Service {
	return &Service{nodeRepo: repo, logger: slog.Default()}
}

func ownedNode(accountID uuid.UUID) *node.Node {
	return &node.Node{ID: uuid.New(), AccountID: accountID}
}

func TestGuardSerialBinding(t *testing.T) {
	ctx := context.Background()
	me := uuid.New()
	other := uuid.New()
	const full = "10000000abcdef12"

	t.Run("unguarded serials skip the repo entirely", func(t *testing.T) {
		repo := &guardFakeRepo{}
		s := guardService(repo)
		for _, serial := range []string{"", "unknown", "0000000000000000"} {
			if err := s.guardSerialBinding(ctx, me, serial); err != nil {
				t.Errorf("serial %q: unexpected error %v", serial, err)
			}
		}
		if repo.calls != 0 || len(repo.lowWordAsked) != 0 {
			t.Errorf("repo queried %d+%d times for unguarded serials, want 0", repo.calls, len(repo.lowWordAsked))
		}
	})

	t.Run("unclaimed full serial passes", func(t *testing.T) {
		s := guardService(&guardFakeRepo{})
		if err := s.guardSerialBinding(ctx, me, full); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("foreign owner rejects with the sentinel", func(t *testing.T) {
		s := guardService(&guardFakeRepo{owners: []*node.Node{ownedNode(other)}})
		err := s.guardSerialBinding(ctx, me, full)
		if !errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got %v, want ErrHardwareSerialConflict", err)
		}
	})

	t.Run("own-account match passes even beside a foreign duplicate", func(t *testing.T) {
		s := guardService(&guardFakeRepo{owners: []*node.Node{ownedNode(other), ownedNode(me)}})
		if err := s.guardSerialBinding(ctx, me, full); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("input is normalized before comparison", func(t *testing.T) {
		s := guardService(&guardFakeRepo{owners: []*node.Node{ownedNode(other)}})
		err := s.guardSerialBinding(ctx, me, "  10000000ABCDEF12 ")
		if !errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got %v, want ErrHardwareSerialConflict for a case-variant serial", err)
		}
	})

	t.Run("repo error is wrapped, never the conflict sentinel", func(t *testing.T) {
		s := guardService(&guardFakeRepo{err: fmt.Errorf("db down")})
		err := s.guardSerialBinding(ctx, me, full)
		if err == nil || errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got %v, want a transient (non-conflict) error", err)
		}
	})

	// The netboot lookup for an eight-hex path collects every row whose serial
	// ends in it, across accounts, so the low word is guarded in both
	// directions: a row ending in another tenant's low word would sit beside
	// that tenant's device on the path its EEPROM asks for.
	foreign := &repository.SerialHolder{AccountID: other, NodeID: uuid.New()}
	for name, serial := range map[string]string{
		"a full serial sharing another account's low word":    "00000000abcdef12",
		"a short stamp another account's full serial ends in": "abcdef12",
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			repo := &guardFakeRepo{holder: foreign}
			err := guardService(repo).guardSerialBinding(ctx, me, serial)
			if !errors.Is(err, app_errors.ErrHardwareSerialConflict) {
				t.Fatalf("got %v, want ErrHardwareSerialConflict", err)
			}
			if len(repo.lowWordAsked) != 1 || repo.lowWordAsked[0] != serial {
				t.Errorf("low-word lookup asked %v, want [%s]", repo.lowWordAsked, serial)
			}
		})
	}

	t.Run("a rebind of a serial the account already holds is not re-judged by its low word", func(t *testing.T) {
		// A reconnect or re-adopt claims nothing new; a collision that
		// predates it must not lock an adopted device out.
		repo := &guardFakeRepo{owners: []*node.Node{ownedNode(me)}, holder: foreign}
		if err := guardService(repo).guardSerialBinding(ctx, me, full); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(repo.lowWordAsked) != 0 {
			t.Errorf("low-word lookup asked %v for a rebind", repo.lowWordAsked)
		}
	})

	t.Run("a degenerate low word is not guarded", func(t *testing.T) {
		repo := &guardFakeRepo{holder: foreign}
		if err := guardService(repo).guardSerialBinding(ctx, me, "1000000000000000"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a low-word lookup error is wrapped, never the conflict sentinel", func(t *testing.T) {
		s := guardService(&guardFakeRepo{lowWordErr: fmt.Errorf("db down")})
		err := s.guardSerialBinding(ctx, me, full)
		if err == nil || errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got %v, want a transient (non-conflict) error", err)
		}
	})
}
