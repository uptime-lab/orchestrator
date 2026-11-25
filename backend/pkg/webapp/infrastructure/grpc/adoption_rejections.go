package grpc

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"google.golang.org/grpc/peer"
)

// Rejected-handshake accounting.
//
// A peer that fails the gRPC handshake is not authenticated, so it has no node
// id, no account, and nothing in the system can attribute it. The agent's
// reconnect backoff does not apply to this case either — the stream is
// ACCEPTED and then rejected at ConnectionInit, which the client reads as a
// server-side disconnect rather than a refusal — so a device holding an expired
// master token retries roughly once a second, forever.
//
// At about one rejection a second, a single such device can produce a large
// share of every line the backend logs. The load is negligible; the damage is
// that the log stops being evidence: the next real failure would be diagnosed
// against a journal that is mostly this loop, and a size-capped container log
// ring would hold little else.
//
// Two things are done about it here, and deliberately not a third:
//
//   - The log line is collapsed. One line per source per window, carrying the
//     count, instead of one line per attempt.
//   - A sustained loop raises an anomaly, so the condition is visible somewhere
//     an operator looks rather than only in a log they have stopped trusting.
//
// What is NOT done: refusing the peer earlier, tarpitting it, or banning the
// address. A rejected handshake costs one token lookup; the retry rate is the
// agent's bug and belongs in the agent, where the fix is to back off on an
// authentication refusal the same way it backs off on a transport failure.
// Blocking the address here would also block the legitimate device the moment
// the operator issues it a fresh token, which is the exact minute it matters.

const (
	// adoptionRejectionLogWindow is how long one source's rejections collapse
	// into a single log line.
	adoptionRejectionLogWindow = 5 * time.Minute
	// adoptionRejectionBurstThreshold is how many rejections from one source in
	// one window count as a loop rather than a device that was retired, moved
	// between accounts, or told to adopt with a token someone had already
	// rotated. A handful of those is ordinary; fifty is a stuck client.
	adoptionRejectionBurstThreshold = 50
	// adoptionRejectionSources bounds the tracking map, for the same reason the
	// auth-burst tracker bounds its own: an attacker able to grow a map one
	// entry per source address turns the watcher into the vulnerability.
	adoptionRejectionSources = 10000

	// adoptionBurstRowWindow / adoptionBurstNewSubjectsPerWindow bound how
	// many DISTINCT burst rows the anomaly table can gain. The subject is
	// peer-supplied and free to vary, and each new subject is a new row in a
	// table nothing here prunes, so a peer cycling serials would otherwise
	// write one row per invented serial. Ten new looping devices an hour is far
	// above what an ordinary fleet produces; past it the rest are folded into one
	// instance-wide row whose occurrence count keeps rising.
	adoptionBurstRowWindow            = time.Hour
	adoptionBurstNewSubjectsPerWindow = 10
	// adoptionBurstNewSubjectsPerSource is the share of that budget one
	// source address may take. The subject is free to invent, so without it a
	// single peer cycling ten made-up serials at the top of every hour took
	// every slot, and a real device that started looping later in the hour
	// was folded into the overflow row, which names no serial. With it a peer
	// needs several addresses to do the same. Behind a proxy that does not
	// pass the client address every device shares one source, and new rows
	// past this share fold into the overflow row sooner; the shipped composes
	// pass it (AGENT_GRPC_PROXY_PROTOCOL).
	adoptionBurstNewSubjectsPerSource = 3
	// adoptionBurstKnownSubjects bounds the set of subjects that already have
	// a row (a repeat burst only bumps that row, so it is always let through).
	adoptionBurstKnownSubjects = 1000
	// adoptionBurstKnownTTL is how long a subject stays known without a burst.
	adoptionBurstKnownTTL = 24 * time.Hour
	// adoptionBurstOverflowSubject keys the instance-wide overflow row. Never
	// a real subject: an empty serial falls back to the source address, which
	// is never empty.
	adoptionBurstOverflowSubject = ""
)

// adoptionRejectionTracker counts recent rejected handshakes per source.
type adoptionRejectionTracker struct {
	mu      sync.Mutex
	sources map[string]*adoptionRejectionCount
	// saturatedLogged throttles the ONE line that replaces per-source logging
	// once the map is full. It lives here rather than in an entry because the
	// condition it describes is about the tracker, not about any one source.
	saturatedLogged time.Time

	// The burst-row budget: subjects that already have a row, and how many
	// new ones this window has admitted. See adoptionBurstNewSubjectsPerWindow.
	burstSubjects       map[string]time.Time
	burstWindowFrom     time.Time
	burstNewInWindow    int
	burstNewBySource    map[string]int
	burstOverflowLogged time.Time
}

type adoptionRejectionCount struct {
	count      int
	windowFrom time.Time
	// lastLogged throttles the log line itself, which is the point of this type.
	lastLogged time.Time
	// reported stops a sustained loop from writing an anomaly per attempt. The
	// row already deduplicates and counts occurrences; writing to it fifty times
	// a minute would reproduce the problem in the database instead of the log.
	reported time.Time
}

func newAdoptionRejectionTracker() *adoptionRejectionTracker {
	return &adoptionRejectionTracker{
		sources:          map[string]*adoptionRejectionCount{},
		burstSubjects:    map[string]time.Time{},
		burstNewBySource: map[string]int{},
	}
}

// admitBurstRow decides where a burst for subject is recorded: in its own row
// (perSubject), folded into the instance-wide overflow row (overflow, at most
// once per log window), or nowhere (both false — the overflow row was written
// moments ago). A subject that already has a row is always let through: its
// burst only bumps that row's count. A new subject takes a slot from the
// window's budget and from its source's share of it.
func (t *adoptionRejectionTracker) admitBurstRow(subject, source string, now time.Time) (perSubject bool, overflow bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.burstSubjects[subject]; ok {
		t.burstSubjects[subject] = now
		return true, false
	}
	if t.burstWindowFrom.IsZero() || now.Sub(t.burstWindowFrom) >= adoptionBurstRowWindow {
		t.burstWindowFrom = now
		t.burstNewInWindow = 0
		// Bounded by the budget: an entry is added only when a slot is.
		clear(t.burstNewBySource)
	}
	if len(t.burstSubjects) >= adoptionBurstKnownSubjects {
		for s, last := range t.burstSubjects {
			if now.Sub(last) >= adoptionBurstKnownTTL {
				delete(t.burstSubjects, s)
			}
		}
	}
	if t.burstNewInWindow < adoptionBurstNewSubjectsPerWindow &&
		t.burstNewBySource[source] < adoptionBurstNewSubjectsPerSource &&
		len(t.burstSubjects) < adoptionBurstKnownSubjects {
		t.burstNewInWindow++
		t.burstNewBySource[source]++
		t.burstSubjects[subject] = now
		return true, false
	}
	if t.burstOverflowLogged.IsZero() || now.Sub(t.burstOverflowLogged) >= adoptionRejectionLogWindow {
		t.burstOverflowLogged = now
		return false, true
	}
	return false, false
}

// note records one rejection and reports what the caller should do about it:
// whether to emit a log line now, the count to put in it, and whether this has
// become a burst worth raising as an anomaly.
func (t *adoptionRejectionTracker) note(source string, now time.Time) (logNow bool, count int, burst bool, saturated bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.sources[source]
	if !ok || now.Sub(entry.windowFrom) > adoptionRejectionLogWindow {
		if len(t.sources) >= adoptionRejectionSources {
			t.evictExpiredLocked(now)
		}
		// Eviction frees NOTHING when every tracked source is still inside its
		// window, and the insert used to happen regardless — so this was not a
		// cap at all: a peer cycling distinct serials, which is free and
		// unauthenticated, grew the map one entry per attempt. maxIdentifierBytes
		// bounds each key's size and never the count, so the two bounds together
		// still multiplied out to unbounded memory.
		//
		// Past the cap the source is simply not tracked. Ten thousand distinct
		// sources rejected inside one five-minute window is not ten thousand
		// stories; it is one, and it is reported as one — which also closes the
		// second half of the same hole, a WARN per unique source, since a fresh
		// window always logs its first rejection and a fresh source is always a
		// fresh window.
		if len(t.sources) >= adoptionRejectionSources {
			if t.saturatedLogged.IsZero() || now.Sub(t.saturatedLogged) >= adoptionRejectionLogWindow {
				t.saturatedLogged = now
				return false, len(t.sources), false, true
			}
			return false, len(t.sources), false, false
		}
		// A fresh window always logs its first rejection: the common case is a
		// one-off (a stale install command, a device moved between accounts) and
		// swallowing it entirely would trade a flood for a blind spot.
		entry = &adoptionRejectionCount{count: 1, windowFrom: now, lastLogged: now}
		t.sources[source] = entry
		return true, 1, false, false
	}

	entry.count++
	if now.Sub(entry.lastLogged) >= adoptionRejectionLogWindow {
		entry.lastLogged = now
		logNow = true
	}

	if entry.count >= adoptionRejectionBurstThreshold &&
		(entry.reported.IsZero() || now.Sub(entry.reported) >= adoptionRejectionLogWindow) {
		entry.reported = now
		burst = true
	}

	return logNow, entry.count, burst, false
}

// evictExpiredLocked drops sources whose window has passed. Called only when the
// map is full, so the ordinary path stays a single map lookup.
func (t *adoptionRejectionTracker) evictExpiredLocked(now time.Time) {
	for source, entry := range t.sources {
		if now.Sub(entry.windowFrom) > adoptionRejectionLogWindow {
			delete(t.sources, source)
		}
	}
}

// noteAdoptionRejection records one rejected handshake and decides what the
// operator sees. reason is a short, stable phrase ("master token expired"),
// never the token itself or anything derived from it.
//
// Logged at WARN, not ERROR: a device presenting a token the platform has
// retired is the platform working, and an ERROR that fires by design is how a
// log stops being read.
//
// serial identifies the DEVICE and is preferred over the address. Without
// PROXY protocol that is forced: gRPC is TLS-terminated at the reverse proxy
// and the TCP router forwards without it, so every agent arrives from the
// proxy's address and keying on it would blend one looping device's count
// with every other device's occasional rejection, and every line would carry
// the proxy's own container address as source_ip. With
// AGENT_GRPC_PROXY_PROTOCOL=require the address is the client's
// (proxy_protocol.go) and the log line becomes attributable, but the serial
// stays the KEY: several devices behind one NAT are still one address, and
// which of them is looping is the question the operator has. The serial is not
// a secret (it is printed on the hardware) and is the same identifier the
// adoption flow already matches nodes on. It is attacker-controllable, hence
// the bounded map.
func (s *Server) noteAdoptionRejection(ctx context.Context, serial, sourceIP, reason string) {
	if s.adoptionRejections == nil {
		return
	}
	// Bounded before it becomes a map key OR reaches a log line. The map is
	// capped at 10 000 entries, which bounds the COUNT and says nothing about
	// the size — and the key is peer-supplied. The caller rejects an over-long
	// serial before this, so in practice nothing here truncates; the bound is
	// kept because this function has three call sites and a future fourth must
	// not have to remember.
	serial = boundedIdentifier(serial)
	subject := strings.TrimSpace(serial)
	if subject == "" || subject == "unknown" {
		subject = sourceIP
	}
	logNow, count, burst, saturated := s.adoptionRejections.note(subject, time.Now())

	if saturated {
		// Deliberately not an anomaly row: the anomaly kinds are keyed per
		// subject, and the whole point of this branch is that there is no
		// subject worth naming — a flood of distinct identifiers. An operator
		// reading this line is being told the tracker stopped distinguishing
		// sources, which is itself the signal.
		s.logger.Warn("adoption handshakes rejected from more distinct sources than the tracker holds; per-source detail suppressed",
			"tracked_sources", count,
			"window_minutes", int(adoptionRejectionLogWindow.Minutes()),
		)
	}

	if logNow {
		s.logger.Warn("adoption handshake rejected",
			"hardware_serial", serial,
			"source_ip", sourceIP,
			"reason", reason,
			"rejections_in_window", count,
			"window_minutes", int(adoptionRejectionLogWindow.Minutes()),
		)
	}

	if !burst || s.anomalyRepo == nil {
		return
	}
	perSubject, overflow := s.adoptionRejections.admitBurstRow(subject, sourceIP, time.Now())
	if overflow {
		// No peer-supplied value in this row: it stands for many subjects,
		// and the details of any one of them are in the log line above.
		anomaly := instance_anomaly.New(
			instance_anomaly.KindAdoptionRejectionBurst,
			instance_anomaly.AdoptionRejectionBurstKey(adoptionBurstOverflowSubject),
			instance_anomaly.SeverityWarning,
			"more distinct devices are repeatedly failing to adopt than are recorded one by one; the rest are counted here — see the backend log for which serials and addresses",
			map[string]interface{}{
				"new_devices_per_hour": adoptionBurstNewSubjectsPerWindow,
				"window_minutes":       int(adoptionRejectionLogWindow.Minutes()),
			},
			nil,
		)
		if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
			s.logger.WarnContext(ctx, "failed to record adoption-rejection anomaly", "error", err)
		}
		return
	}
	if !perSubject {
		return
	}
	anomaly := instance_anomaly.New(
		instance_anomaly.KindAdoptionRejectionBurst,
		instance_anomaly.AdoptionRejectionBurstKey(subject),
		instance_anomaly.SeverityWarning,
		"a device is repeatedly failing to adopt and will keep retrying — most often a master token that expired or was revoked, on a device nobody is watching",
		map[string]interface{}{
			"hardware_serial": serial,
			"source_ip":       sourceIP,
			"reason":          reason,
			"rejections":      count,
			"window_minutes":  int(adoptionRejectionLogWindow.Minutes()),
		},
		nil,
	)
	if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
		s.logger.WarnContext(ctx, "failed to record adoption-rejection anomaly", "error", err)
	}
}

// peerIP returns the source address of the current stream, or "unknown" when the
// transport does not expose one. Used only as a grouping key and in details.
//
// The port is stripped: a retrying client gets a fresh source port every
// attempt, so keying on host:port would defeat the collapsing this whole file
// exists to do. Without PROXY protocol every agent arrives from the proxy's
// address, which makes the key coarse rather than wrong — the anomaly's job is
// "some device is looping", and the reason field is what identifies which.
// With AGENT_GRPC_PROXY_PROTOCOL=require it is the real client address. The per-source pre-auth gate uses peerSourceKey
// (proxy_protocol.go) rather than this, because it must fold v4-in-v6 spellings
// into one bucket; this stays a human-readable label for log and anomaly rows.
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}
