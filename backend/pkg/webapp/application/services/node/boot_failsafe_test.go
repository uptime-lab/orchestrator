package node

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// failsafeFakeRepo stubs only the two atomic methods RegisterNetbootWithhold
// touches; the embedded nil interface panics on any other call, proving the
// decision reads/writes nothing else.
type failsafeFakeRepo struct {
	repository.NodeRepository
	incCount int
	incCalls int
	setCalls int
}

func (f *failsafeFakeRepo) IncrementBootAttempt(_ context.Context, _ uuid.UUID, _, _ time.Duration) (int, bool, error) {
	f.incCalls++
	return f.incCount, true, nil
}

func (f *failsafeFakeRepo) SetBootFailsafe(_ context.Context, _ uuid.UUID, _ time.Time, _ int) error {
	f.setCalls++
	return nil
}

func failsafeNode() *node.Node { return &node.Node{ID: uuid.New(), AccountID: uuid.New()} }

func TestRegisterNetbootWithhold(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled (threshold 0) never counts", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 99}
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, failsafeNode(), 0)
		if err != nil || serve {
			t.Fatalf("got serve=%v err=%v, want false/nil", serve, err)
		}
		if repo.incCalls != 0 {
			t.Errorf("counted %d times while disabled, want 0", repo.incCalls)
		}
	})

	t.Run("a live agent suppresses counting", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 99}
		n := failsafeNode()
		now := time.Now()
		n.LastHeartbeat = &now // recently alive -> not crash-looping
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, n, 3)
		if err != nil || serve {
			t.Fatalf("got serve=%v err=%v, want false/nil", serve, err)
		}
		if repo.incCalls != 0 {
			t.Errorf("counted %d times for a live node, want 0", repo.incCalls)
		}
	})

	t.Run("below threshold withholds", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 2}
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, failsafeNode(), 3)
		if err != nil || serve {
			t.Fatalf("got serve=%v err=%v, want false/nil", serve, err)
		}
		if repo.setCalls != 0 {
			t.Errorf("set failsafe below threshold (%d calls), want 0", repo.setCalls)
		}
	})

	t.Run("at threshold serves recovery and flags it", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 3}
		n := failsafeNode()
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, n, 3)
		if err != nil || !serve {
			t.Fatalf("got serve=%v err=%v, want true/nil", serve, err)
		}
		if repo.setCalls != 1 {
			t.Errorf("SetBootFailsafe called %d times, want 1", repo.setCalls)
		}
		if n.BootFailsafeAt == nil {
			t.Errorf("BootFailsafeAt not set on the returned node")
		}
	})

	t.Run("a stale heartbeat does not suppress counting", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 3}
		n := failsafeNode()
		old := time.Now().Add(-30 * time.Minute)
		n.LastHeartbeat = &old
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, n, 3)
		if err != nil || !serve {
			t.Fatalf("got serve=%v err=%v, want true/nil", serve, err)
		}
	})

	t.Run("per-node override beats the global default", func(t *testing.T) {
		repo := &failsafeFakeRepo{incCount: 2}
		n := failsafeNode()
		two := 2
		n.BootFailsafeThreshold = &two // override 2 beats global 0 (disabled)
		serve, err := guardService(repo).RegisterNetbootWithhold(ctx, n, 0)
		if err != nil || !serve {
			t.Fatalf("got serve=%v err=%v, want true/nil (override should enable)", serve, err)
		}
	})
}
