package grpc

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
)

// The point of the tracker is that a device looping once a second produces one
// log line per window instead of three hundred. These pin that, because the
// failure mode of getting it wrong is silent in one direction (a flood returns)
// and worse in the other (the condition stops being reported at all).

func TestFirstRejectionInAWindowAlwaysLogs(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()

	logNow, count, burst, _ := tr.note("serial-a", now)
	if !logNow {
		t.Fatal("the first rejection from a source must log: the common case is a one-off, and swallowing it trades a flood for a blind spot")
	}
	if count != 1 || burst {
		t.Fatalf("first rejection: got count=%d burst=%v, want 1/false", count, burst)
	}
}

func TestRepeatsInsideTheWindowAreCountedButNotLogged(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()
	tr.note("serial-a", start)

	logged := 0
	for i := 1; i < 40; i++ {
		// One a second, the agent's retry rate.
		if logNow, _, _, _ := tr.note("serial-a", start.Add(time.Duration(i)*time.Second)); logNow {
			logged++
		}
	}
	if logged != 0 {
		t.Fatalf("39 further rejections inside the window logged %d lines, want 0", logged)
	}
}

func TestTheWindowReopensAndCarriesTheCount(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()
	tr.note("serial-a", start)
	for i := 1; i < 10; i++ {
		tr.note("serial-a", start.Add(time.Duration(i)*time.Second))
	}

	// Still inside the window: one line, and it must carry the accumulated
	// count, otherwise the operator sees "rejected" and not "rejected 300 times".
	logNow, count, _, _ := tr.note("serial-a", start.Add(adoptionRejectionLogWindow))
	if !logNow {
		t.Fatal("a rejection a full window after the last logged one must log again")
	}
	if count != 11 {
		t.Fatalf("got count=%d, want 11 — the line must report how many happened, not just that one did", count)
	}
}

func TestBurstIsRaisedOnceNotPerAttempt(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()

	bursts := 0
	for i := 0; i < adoptionRejectionBurstThreshold*3; i++ {
		if _, _, burst, _ := tr.note("serial-a", start.Add(time.Duration(i)*time.Second)); burst {
			bursts++
		}
	}
	if bursts != 1 {
		t.Fatalf("got %d bursts, want exactly 1 — writing an anomaly per attempt past the threshold reproduces the flood in the database", bursts)
	}
}

func TestBelowThresholdNeverBursts(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()

	for i := 0; i < adoptionRejectionBurstThreshold-1; i++ {
		if _, _, burst, _ := tr.note("serial-a", start.Add(time.Duration(i)*time.Second)); burst {
			t.Fatalf("burst raised at attempt %d, below the threshold of %d — a handful of rejections is an ordinary retired device", i+1, adoptionRejectionBurstThreshold)
		}
	}
}

func TestSourcesAreTrackedIndependently(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()

	// One device looping must not push an unrelated device over the threshold.
	// This is the whole reason the subject is the hardware serial rather than
	// the peer address, which is shared by every agent behind Traefik.
	for i := 0; i < adoptionRejectionBurstThreshold*2; i++ {
		tr.note("serial-loud", start.Add(time.Duration(i)*time.Second))
	}
	logNow, count, burst, _ := tr.note("serial-quiet", start.Add(time.Second))
	if !logNow || count != 1 || burst {
		t.Fatalf("quiet source: got logNow=%v count=%d burst=%v, want true/1/false", logNow, count, burst)
	}
}

func TestEvictionKeepsTheMapBounded(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	start := time.Now()

	for i := 0; i < adoptionRejectionSources; i++ {
		tr.note(string(rune(i%1000))+"-"+time.Duration(i).String(), start)
	}
	// Everything above is stale by now, so the next insert must be able to
	// reclaim rather than grow without bound — an attacker sending a fresh
	// serial per attempt would otherwise turn the watcher into the vulnerability.
	tr.note("fresh", start.Add(2*adoptionRejectionLogWindow))
	if len(tr.sources) > adoptionRejectionSources {
		t.Fatalf("map grew to %d entries, above the %d bound", len(tr.sources), adoptionRejectionSources)
	}
}

// The case the bound was actually missing: distinct sources arriving FASTER
// than the window retires them.
//
// TestEvictionKeepsTheMapBounded above covers the easy half — everything is
// stale, so eviction reclaims — and it passed throughout the life of the bug.
// When every tracked source is still inside its window, evictExpiredLocked
// frees nothing and the insert used to happen anyway, so the map grew one entry
// per unauthenticated attempt. maxIdentifierBytes bounds each key's SIZE and
// says nothing about the count; a peer cycling serials pays nothing for either.
func TestMapDoesNotGrowWhenNothingCanBeEvicted(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()

	// Twice the cap, all within one window, so no entry is ever evictable.
	for i := 0; i < adoptionRejectionSources*2; i++ {
		tr.note("serial-"+strconv.Itoa(i), now)
	}
	if len(tr.sources) > adoptionRejectionSources {
		t.Fatalf("map grew to %d entries with nothing evictable, above the %d bound", len(tr.sources), adoptionRejectionSources)
	}
}

// The other half of the same hole: a fresh source always logged its first
// rejection, and a peer cycling serials is nothing but fresh sources — so the
// per-source throttle never applied and the flood reached the log. Past
// saturation the tracker stops distinguishing sources and says so once per
// window instead.
func TestSaturationReplacesPerSourceLoggingWithOneLine(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()

	for i := 0; i < adoptionRejectionSources; i++ {
		tr.note("serial-"+strconv.Itoa(i), now)
	}

	logLines, saturationLines := 0, 0
	for i := 0; i < 1000; i++ {
		logNow, _, _, saturated := tr.note("flood-"+strconv.Itoa(i), now.Add(time.Second))
		if logNow {
			logLines++
		}
		if saturated {
			saturationLines++
		}
	}
	if logLines != 0 {
		t.Fatalf("past the cap %d sources still wrote a per-source log line; the flood reaches the log ring that holds every other tenant's evidence", logLines)
	}
	if saturationLines != 1 {
		t.Fatalf("got %d saturation lines for one window, want exactly 1 — the replacement must not become the flood it replaced", saturationLines)
	}
}

// A burst row is keyed on the subject, and the subject is peer-supplied: a
// peer cycling serials past the burst threshold must not be able to add one
// anomaly row per invented serial. New subjects get their own row only up to a
// budget per window; the rest fold into one instance-wide row.

type burstRecordingAnomalyRepo struct {
	repository.InstanceAnomalyRepository
	keys map[string]int
}

func (r *burstRecordingAnomalyRepo) Record(_ context.Context, a *instance_anomaly.Anomaly) error {
	r.keys[a.DedupeKey]++
	return nil
}

func TestRotatingSerialsCannotAddUnboundedBurstRows(t *testing.T) {
	anomalies := &burstRecordingAnomalyRepo{keys: map[string]int{}}
	s := &Server{
		adoptionRejections: newAdoptionRejectionTracker(),
		anomalyRepo:        anomalies,
		logger:             slog.New(slog.DiscardHandler),
	}

	const serials = 200
	for i := 0; i < serials; i++ {
		serial := "rotated-" + strconv.Itoa(i)
		for j := 0; j < adoptionRejectionBurstThreshold; j++ {
			s.noteAdoptionRejection(context.Background(), serial, "198.51.100.7", "invalid master token")
		}
	}

	if got, max := len(anomalies.keys), adoptionBurstNewSubjectsPerWindow+1; got > max {
		t.Fatalf("%d distinct serials past the threshold wrote %d distinct anomaly rows, want at most %d (the budget plus one overflow row)", serials, got, max)
	}
	if anomalies.keys[instance_anomaly.AdoptionRejectionBurstKey(adoptionBurstOverflowSubject)] == 0 {
		t.Error("the subjects past the budget were not recorded at all; they must fold into the overflow row")
	}
}

func TestAKnownLoopingDeviceKeepsItsOwnRowPastTheBudget(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()

	if perSubject, _ := tr.admitBurstRow("looping-device", "192.0.2.1", now); !perSubject {
		t.Fatal("the first burst subject was not given its own row")
	}
	for i := 0; i < adoptionBurstNewSubjectsPerWindow*3; i++ {
		tr.admitBurstRow("flood-"+strconv.Itoa(i), "198.51.100."+strconv.Itoa(i), now)
	}
	if perSubject, _ := tr.admitBurstRow("looping-device", "192.0.2.1", now.Add(adoptionRejectionLogWindow)); !perSubject {
		t.Error("a device that already has a row lost it to the budget; its repeat burst only bumps that row")
	}
}

func TestTheBurstRowBudgetReopensAfterItsWindow(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()
	for i := 0; i < adoptionBurstNewSubjectsPerWindow; i++ {
		tr.admitBurstRow("first-"+strconv.Itoa(i), "198.51.100."+strconv.Itoa(i), now)
	}
	if perSubject, _ := tr.admitBurstRow("over-budget", "192.0.2.1", now); perSubject {
		t.Fatal("a subject past the budget got its own row")
	}
	if perSubject, _ := tr.admitBurstRow("next-window", "192.0.2.1", now.Add(adoptionBurstRowWindow)); !perSubject {
		t.Error("a new subject in the next window was refused its own row")
	}
}

// The subject is peer-supplied. One source inventing serials at the top of
// every hour must not take every slot, or a real device that starts looping
// later in the hour is folded into the overflow row, which names no serial.
func TestOneSourceCannotTakeTheWholeBurstRowBudget(t *testing.T) {
	tr := newAdoptionRejectionTracker()
	now := time.Now()
	for i := 0; i < adoptionBurstNewSubjectsPerWindow; i++ {
		tr.admitBurstRow("invented-"+strconv.Itoa(i), "203.0.113.9", now)
	}
	if perSubject, _ := tr.admitBurstRow("real-device", "192.0.2.44", now.Add(time.Minute)); !perSubject {
		t.Fatal("a real device from another source was folded into the overflow row after one source invented serials")
	}
}
