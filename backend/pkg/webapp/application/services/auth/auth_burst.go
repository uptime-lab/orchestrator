package auth

import (
	"context"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
)

// Credential-stuffing detection (the "auth-abuse burst" anomaly).
//
// Nothing recorded a failed sign-in before this. Audit events are account-scoped
// and a failed login has no account — that is the whole point of it — so the
// operator's only evidence of someone working through a password list was a log
// line per attempt, which nobody reads until after the fact.
//
// Counted in memory rather than written per attempt. A table of failures is a
// table an attacker controls the size of, and it would hold an email and an IP
// for every guess; the operator's actual question is "is someone hammering this
// instance right now", which a counter answers without keeping the attempts.
// The cost is that the count resets on restart and is per-process — acceptable
// while an install runs a single backend process, and a burst that survives a
// restart re-arms within the window anyway.

const (
	// authBurstWindow is how long failures accumulate for one source.
	authBurstWindow = 10 * time.Minute
	// authBurstThreshold is how many failures in that window count as a burst.
	// Well above a person mistyping a password twice and reaching for their
	// password manager, well below what a scripted attempt produces in seconds.
	authBurstThreshold = 20
	// authBurstSources bounds the tracking map. A distributed attempt would
	// otherwise let an attacker grow it one entry per source address, which is
	// a memory-exhaustion vector inside the very code that watches for abuse.
	authBurstSources = 10000
)

// authBurstTracker counts recent authentication failures per source.
type authBurstTracker struct {
	mu      sync.Mutex
	sources map[string]*authBurstCount
}

type authBurstCount struct {
	count      int
	windowFrom time.Time
	// reported stops one sustained burst from recording an anomaly every
	// attempt past the threshold. The anomaly row already deduplicates and
	// counts occurrences, but writing to it thousands of times a minute is
	// exactly the load the attacker is generating.
	reported time.Time
}

func newAuthBurstTracker() *authBurstTracker {
	return &authBurstTracker{sources: map[string]*authBurstCount{}}
}

// note records one failure and reports whether this is a burst worth raising.
func (t *authBurstTracker) note(source string, now time.Time) (burst bool, count int) {
	if source == "" {
		return false, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.sources[source]
	if !ok {
		if len(t.sources) >= authBurstSources {
			t.evictExpiredLocked(now)
		}
		if len(t.sources) >= authBurstSources {
			// Still full: every entry is live, which is itself the abuse case.
			// Dropping the new source is better than growing without bound —
			// the sources already tracked are raising the alarm regardless.
			return false, 0
		}
		entry = &authBurstCount{windowFrom: now}
		t.sources[source] = entry
	}
	if now.Sub(entry.windowFrom) > authBurstWindow {
		entry.count = 0
		entry.windowFrom = now
		entry.reported = time.Time{}
	}
	entry.count++
	if entry.count < authBurstThreshold {
		return false, entry.count
	}
	if !entry.reported.IsZero() && now.Sub(entry.reported) < authBurstWindow {
		return false, entry.count
	}
	entry.reported = now
	return true, entry.count
}

// evictExpiredLocked drops sources whose window has passed. Called only when the
// map is full, so the ordinary path stays a single map lookup.
func (t *authBurstTracker) evictExpiredLocked(now time.Time) {
	for source, entry := range t.sources {
		if now.Sub(entry.windowFrom) > authBurstWindow {
			delete(t.sources, source)
		}
	}
}

// NoteFailedLogin records one rejected credential attempt from a client.
//
// Called by the HTTP layer, which is the only place that knows the source
// address — the same reason session device labels are attached there. Every
// credential-verifying path should call it: a detector that watches only the
// password form tells an attacker which door is unwatched.
func (s *AuthService) NoteFailedLogin(ctx context.Context, sourceIP string) {
	if s.authBurst == nil {
		return
	}
	burst, count := s.authBurst.note(sourceIP, time.Now())
	if !burst || s.anomalyRepo == nil {
		return
	}
	anomaly := instance_anomaly.New(
		instance_anomaly.KindAuthFailureBurst,
		instance_anomaly.AuthFailureBurstKey(sourceIP),
		instance_anomaly.SeverityWarning,
		"one client has failed authentication repeatedly in a short window, which is what working through a credential list looks like",
		map[string]interface{}{
			"source_ip":      sourceIP,
			"failures":       count,
			"window_minutes": int(authBurstWindow.Minutes()),
		},
		nil,
	)
	if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
		s.logger.WarnContext(ctx, "failed to record auth-burst anomaly", "error", err)
	}
}
