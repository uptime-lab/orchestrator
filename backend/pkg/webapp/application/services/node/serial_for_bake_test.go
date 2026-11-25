package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// bakeFakeRepo answers the exact lookup from owners and, separately, offers the
// suffix-matching lookup the real repository performs — so a test can tell the
// two apart. Any other repo call panics through the embedded nil interface.
type bakeFakeRepo struct {
	repository.NodeRepository
	owners      []*node.Node
	suffixMatch *node.Node
	suffixCalls int
	exactSerial string
	// heldElsewhere answers the low-word question: another account holds a
	// serial colliding with the asked one by its low word.
	heldElsewhere bool
	heldAsked     string
}

func (f *bakeFakeRepo) LowWordHolderInAnotherAccount(_ context.Context, _ uuid.UUID, serial string) (*repository.SerialHolder, error) {
	f.heldAsked = serial
	if !f.heldElsewhere {
		return nil, nil
	}
	return &repository.SerialHolder{AccountID: uuid.New(), NodeID: uuid.New()}, nil
}

func (f *bakeFakeRepo) ListByExactSerial(_ context.Context, serial string) ([]*node.Node, error) {
	f.exactSerial = serial
	return f.owners, nil
}

func (f *bakeFakeRepo) GetByHardwareSerial(_ context.Context, _ uuid.UUID, _ string) (*node.Node, error) {
	f.suffixCalls++
	if f.suffixMatch == nil {
		return nil, app_errors.ErrNodeNotFound
	}
	return f.suffixMatch, nil
}

func serialOwner(accountID uuid.UUID) *node.Node {
	return &node.Node{ID: uuid.New(), AccountID: accountID}
}

func TestResolveSerialForBake(t *testing.T) {
	ctx := context.Background()
	me := uuid.New()
	other := uuid.New()
	const short = "abcdef12"
	const full = "10000000abcdef12"

	t.Run("a serial owned by another account is refused", func(t *testing.T) {
		repo := &bakeFakeRepo{owners: []*node.Node{serialOwner(other)}}
		s := &Service{nodeRepo: repo, logger: slog.Default()}

		found, err := s.ResolveSerialForBake(ctx, me, full)
		if !errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got (%v, %v), want ErrHardwareSerialConflict", found, err)
		}
		if found != nil {
			t.Errorf("a refused serial must resolve to no node, got %v", found.ID)
		}
	})

	t.Run("a full serial no node carries exactly does not fall back to a suffix match", func(t *testing.T) {
		repo := &bakeFakeRepo{suffixMatch: serialOwner(me)}
		s := &Service{nodeRepo: repo, logger: slog.Default()}

		if _, err := s.ResolveSerialForBake(ctx, me, full); !errors.Is(err, app_errors.ErrNodeNotFound) {
			t.Fatalf("got %v, want ErrNodeNotFound", err)
		}
		if repo.suffixCalls != 0 {
			t.Errorf("the suffix lookup was consulted %d time(s); a full-serial bake must decide on an exact match", repo.suffixCalls)
		}
	})

	t.Run("the account's own node answers its exact serial", func(t *testing.T) {
		own := serialOwner(me)
		repo := &bakeFakeRepo{owners: []*node.Node{serialOwner(other), own}}
		s := &Service{nodeRepo: repo, logger: slog.Default()}

		found, err := s.ResolveSerialForBake(ctx, me, "  10000000ABCDEF12 ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found.ID != own.ID {
			t.Errorf("resolved node %v, want the caller's own %v", found.ID, own.ID)
		}
		if repo.exactSerial != full {
			t.Errorf("looked up %q, want the normalized %q", repo.exactSerial, full)
		}
	})

	// A WebUSB harvest only sees the low word, so the short form must still
	// reconcile against a node stamped with the full serial.
	t.Run("a short harvest still reconciles by suffix", func(t *testing.T) {
		own := serialOwner(me)
		repo := &bakeFakeRepo{suffixMatch: own}
		s := &Service{nodeRepo: repo, logger: slog.Default()}

		found, err := s.ResolveSerialForBake(ctx, me, short)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found.ID != own.ID || repo.suffixCalls != 1 {
			t.Errorf("resolved %v after %d suffix lookup(s), want %v after 1", found, repo.suffixCalls, own.ID)
		}
	})

	// The low word of a device adopted in another account: a stamp here would
	// put this account's row on the path that device's EEPROM asks for.
	t.Run("a short serial whose full form another account holds is refused", func(t *testing.T) {
		repo := &bakeFakeRepo{heldElsewhere: true, suffixMatch: serialOwner(me)}
		s := &Service{nodeRepo: repo, logger: slog.Default()}

		found, err := s.ResolveSerialForBake(ctx, me, " ABCDEF12 ")
		if !errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			t.Fatalf("got (%v, %v), want ErrHardwareSerialConflict", found, err)
		}
		if repo.heldAsked != short {
			t.Errorf("asked about %q, want the normalized %q", repo.heldAsked, short)
		}
		if repo.suffixCalls != 0 {
			t.Errorf("a refused serial must not be resolved, got %d suffix lookup(s)", repo.suffixCalls)
		}
	})
}
