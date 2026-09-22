package job

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/logger"

	"github.com/op/go-logging"
)

// The job logs on the backoff path, and the package logger is nil until initialised —
// dereferencing it panics (the same trap main.initLogger exists to avoid for the CLI).
func init() { logger.InitLogger(logging.CRITICAL) }

// fakeXray is a core that is down, and that comes back up only when told to.
type fakeXray struct {
	running  bool
	restarts int
	// recoverAfter: the restart attempt number that finally succeeds. 0 = never.
	recoverAfter int
	restartErr   error
}

func (f *fakeXray) DidXrayCrash() bool  { return !f.running }
func (f *fakeXray) IsXrayRunning() bool { return f.running }
func (f *fakeXray) RestartXray(bool) error {
	f.restarts++
	if f.recoverAfter > 0 && f.restarts >= f.recoverAfter {
		f.running = true
	}
	return f.restartErr
}

func newJobWith(f *fakeXray) *CheckXrayRunningJob {
	j := NewCheckXrayRunningJob()
	j.health = f
	return j
}

// tick runs the job n times, i.e. simulates n seconds of the @every 1s schedule.
func tick(j *CheckXrayRunningJob, n int) {
	for i := 0; i < n; i++ {
		j.Run()
	}
}

// The bug: a core that can never start (bad config, port taken) was restarted every
// 2 seconds forever, and each attempt rebuilds the whole Xray config from the DB.
// Over a minute that was 30 full config generations; it is what pegged the CPU.
func TestRestartsBackOffWhileXrayStaysDown(t *testing.T) {
	f := &fakeXray{} // never recovers
	j := newJobWith(f)

	tick(j, 60)

	// Flat 2s retries would be 30 attempts in 60 ticks. With doubling (2,4,8,16,32,60)
	// it must be far fewer.
	if f.restarts > 8 {
		t.Fatalf("%d restart attempts in 60s — the backoff is not holding (flat 2s would be 30)", f.restarts)
	}
	if f.restarts == 0 {
		t.Fatal("no restart attempted at all; a crashed core must still be retried")
	}
}

// The backoff must not slow down the case it was never meant to touch: a core that
// genuinely just crashed and comes back on the first restart.
func TestFirstRestartIsStillFast(t *testing.T) {
	f := &fakeXray{recoverAfter: 1}
	j := newJobWith(f)

	tick(j, 2)

	if f.restarts != 1 {
		t.Fatalf("restarts=%d after 2 ticks, want 1: the first retry must stay at %ds",
			f.restarts, xrayRestartBackoffMin)
	}
	if !f.running {
		t.Fatal("xray should be running again")
	}
}

// Once it recovers, a later outage must get the fast path again rather than inheriting
// the long delay from the previous one.
func TestBackoffResetsAfterRecovery(t *testing.T) {
	f := &fakeXray{}
	j := newJobWith(f)

	tick(j, 40) // drive the delay up
	widened := f.restarts

	f.running = true
	j.Run() // seen healthy -> reset
	f.running = false
	f.recoverAfter = f.restarts + 1

	tick(j, xrayRestartBackoffMin)
	if f.restarts != widened+1 {
		t.Fatalf("after recovery the next outage took %d ticks to retry; it must be back to %d",
			f.restarts-widened, xrayRestartBackoffMin)
	}
}

// A core that starts and immediately exits returns NO error from RestartXray, so the
// backoff cannot key on err alone — that is the expensive case.
func TestBackoffAppliesWhenRestartReportsSuccessButCoreStaysDown(t *testing.T) {
	f := &fakeXray{restartErr: nil} // "succeeds", never runs
	j := newJobWith(f)

	tick(j, 60)

	if f.restarts > 8 {
		t.Fatalf("%d attempts: a silently-failing restart is still looping at full speed", f.restarts)
	}
}

// The delay must never grow without bound.
func TestBackoffIsCapped(t *testing.T) {
	f := &fakeXray{}
	j := newJobWith(f)

	tick(j, 2000)

	if j.waitTicks > xrayRestartBackoffMax {
		t.Fatalf("waitTicks=%d exceeded the %ds cap", j.waitTicks, xrayRestartBackoffMax)
	}
	// And it must still be retrying at the cap, not have given up.
	before := f.restarts
	tick(j, xrayRestartBackoffMax+1)
	if f.restarts == before {
		t.Fatal("at the cap the job stopped retrying entirely")
	}
}
