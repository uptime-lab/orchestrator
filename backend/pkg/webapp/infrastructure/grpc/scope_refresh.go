package grpc

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// scopeRefreshTimeout bounds the committed-row read behind a live scope
// refresh. The read runs on the assignment request path (UpdateNodeOU) and on
// the node's own message loop (registration), so it must not be allowed to hang
// either on a stalled database.
const scopeRefreshTimeout = 3 * time.Second

// refreshConnectionScope re-reads the node's COMMITTED organizational unit and
// publishes it as the connection's live scope.
//
// Invariant: the live scope always equals the last committed value of the row.
// Two races broke that when the scope was written from the caller's argument:
//
//	(a) registration read the unit, an assignment committed a new one and
//	    called UpdateNodeOU — which found no connection yet, or the displaced
//	    old one — and registration then published the value it had read;
//	(b) two assignments committed A then B but applied B then A in memory.
//
// Both are the same defect: "read the committed value" and "publish it" were
// not one step, so a publish could carry a value older than one already
// committed. Here they are one step, under the connection's own scopeMu, and
// the value published is never the argument but what the row says NOW. Every
// caller runs AFTER its own commit (the assignment path calls this after
// AssignNodeOU returns; registration calls it after the connection is in the
// registry, so any later assignment finds it), which gives the ordering
// argument: the refresh that applies last took its read last, and a read that
// happens after a commit observes it — so the last applier always observes a
// value at least as new as any committed one, whatever order the callers ran
// in. The registration's pre-publish read still fills the first value so the
// first metrics frame is scoped; the post-publish refresh is what makes it
// converge.
//
// The lock is per CONNECTION and never s.mu: s.mu guards the registry map, is
// taken by every inbound frame, and must never wait on the database. The
// OUID field itself keeps its existing discipline — read by the fan-out under
// s.mu.RLock, written here under a short s.mu.Lock once the read has completed
// outside it.
//
// On a read failure the scope FAILS CLOSED: it is narrowed to "unassigned",
// which the fan-out delivers to account-wide holders only (they may read every
// node, so nothing widens), the failure is logged naming the node, and the read
// is retried a bounded number of times. Neither the previous copy nor the
// caller's argument is trusted: the argument may already have been overtaken by
// a later commit (race (b)), and the previous copy is exactly the stale unit a
// move away from it must stop feeding — kept, it would let a database
// hiccup during a move leave the OLD unit's viewers on the node's telemetry
// until the next assignment or reconnect. A row that no longer exists
// is treated the same way — the heartbeat path ends that stream anyway. The
// cost of failing closed is a stale panel for the NEW unit's viewers until a
// retry, the next assignment or a reconnect succeeds.
func (s *Server) refreshConnectionScope(parent context.Context, conn *StreamConnection, fallback *uuid.UUID) {
	s.refreshScopeAttempt(context.WithoutCancel(parent), conn, fallback, scopeRefreshRetries)
}

// scopeRefreshRetries is how many times a failed committed-row read is retried
// (after scopeRefreshRetryDelay each); scopeRefreshRetryDelay is a variable so a
// test can drive the retry without waiting.
const scopeRefreshRetries = 3

var scopeRefreshRetryDelay = 5 * time.Second

func (s *Server) refreshScopeAttempt(ctx context.Context, conn *StreamConnection, fallback *uuid.UUID, retriesLeft int) {
	if conn == nil || s.nodeService == nil {
		return
	}
	nodeUUID, err := uuid.Parse(conn.NodeID)
	if err != nil {
		return
	}
	accountUUID, err := uuid.Parse(conn.AccountID)
	if err != nil {
		return
	}

	conn.scopeMu.Lock()
	defer conn.scopeMu.Unlock()

	readCtx, cancel := context.WithTimeout(ctx, scopeRefreshTimeout)
	defer cancel()
	n, err := s.nodeService.GetByIDAndAccount(readCtx, nodeUUID, accountUUID)
	if err != nil || n == nil {
		s.mu.Lock()
		conn.OUID = nil
		s.mu.Unlock()
		s.logger.WarnContext(ctx, "live OU scope refresh could not read the node row; narrowed the scope to account-wide viewers until a retry succeeds",
			"node_id", conn.NodeID,
			"account_id", conn.AccountID,
			"intended_ou_id", uuidPtrString(fallback),
			"retries_left", retriesLeft,
			"error", err)
		if retriesLeft > 0 {
			time.AfterFunc(scopeRefreshRetryDelay, func() {
				// Only while this connection is still the registered one: a
				// displaced or torn-down stream must not be refreshed into a
				// map slot a newer connection now owns.
				s.mu.RLock()
				current := s.connections[conn.NodeID]
				s.mu.RUnlock()
				if current != conn {
					return
				}
				s.refreshScopeAttempt(ctx, conn, fallback, retriesLeft-1)
			})
		}
		return
	}

	s.mu.Lock()
	conn.OUID = n.OUID
	s.mu.Unlock()
}

// uuidPtrString renders an optional id for a log attribute.
func uuidPtrString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
