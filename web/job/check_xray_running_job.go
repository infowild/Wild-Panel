// Package job provides background job implementations for the vpn-ui web panel,
// including traffic monitoring, system checks, and periodic maintenance tasks.
package job

import (
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// Backoff bounds for the respawn loop below. The job ticks every second, so these are
// in ticks: wait 2 before the first restart, then double the wait after each restart
// that did not bring Xray up, up to a minute.
//
// An unbounded 2-second respawn is what these exist to stop. A crashed Xray is usually
// a crashed PROCESS (restart fixes it), but it is just as often a config the core
// REFUSES: a routing rule naming a geo file that will not download, a port another
// daemon took, an outbound an operator just saved wrong. In that state Xray can never
// come up, and the old code retried forever at a fixed 2 seconds with no ceiling.
//
// That is not a cheap poll. Every attempt runs XrayService.RestartXray, which rebuilds
// the entire config from scratch — reading every inbound, parsing every client's
// settings JSON, rescanning the config for geo references — because the "config
// unchanged, skip" short-circuit inside it is guarded on Xray ALREADY RUNNING and so
// never applies while it is down. On a panel with a few hundred accounts that is a
// core pegged at 100% indefinitely, plus an error line in the log every two seconds,
// starting the moment the config went bad and never stopping on its own. That is the
// "the panel suddenly eats CPU" report.
//
// Backing off keeps a genuine crash fast (a real one recovers on the first restart and
// the counters reset) while a permanently-broken config settles to one attempt a
// minute, which still self-heals the moment an operator fixes it.
const (
	xrayRestartBackoffMin = 2
	xrayRestartBackoffMax = 60
)

// xrayHealth is the slice of XrayService this job drives. An interface purely so the
// backoff above can be tested without a real core: the production value is always
// *service.XrayService.
type xrayHealth interface {
	DidXrayCrash() bool
	RestartXray(isForce bool) error
	IsXrayRunning() bool
}

// CheckXrayRunningJob monitors Xray process health and restarts it if it crashes.
type CheckXrayRunningJob struct {
	xrayService service.XrayService
	health      xrayHealth
	checkTime   int
	// waitTicks is how many consecutive down-ticks must pass before the next restart
	// attempt. Doubles on each failed attempt, resets as soon as Xray is seen running.
	waitTicks int
}

// NewCheckXrayRunningJob creates a new Xray health check job instance.
func NewCheckXrayRunningJob() *CheckXrayRunningJob {
	j := &CheckXrayRunningJob{waitTicks: xrayRestartBackoffMin}
	j.health = &j.xrayService
	return j
}

// Run checks if Xray has crashed and restarts it once it has been down for waitTicks
// consecutive checks, backing off while restarts keep failing to bring it up.
func (j *CheckXrayRunningJob) Run() {
	if j.health == nil {
		j.health = &j.xrayService // zero value from a bare struct literal
	}
	if j.waitTicks < xrayRestartBackoffMin {
		j.waitTicks = xrayRestartBackoffMin
	}
	if !j.health.DidXrayCrash() {
		// Up: forget the outage entirely, so the next one gets the fast path again.
		j.checkTime = 0
		j.waitTicks = xrayRestartBackoffMin
		return
	}

	j.checkTime++
	if j.checkTime < j.waitTicks {
		return
	}
	j.checkTime = 0

	err := j.health.RestartXray(false)
	if err != nil {
		// RestartXray reported a failure: the config did not even produce a process.
		logger.Error("Restart xray failed:", err)
	}
	if j.health.IsXrayRunning() {
		// Came up. Next outage starts from the short wait again.
		j.waitTicks = xrayRestartBackoffMin
		return
	}
	// Still down. Whether RestartXray returned an error or not, the restart did not
	// achieve anything, so slow the next one down. Checked via IsXrayRunning rather
	// than on err alone because a core that starts and immediately exits on a bad
	// config returns no error here — that is the expensive case, and keying on err
	// would leave it looping at full speed.
	if j.waitTicks < xrayRestartBackoffMax {
		j.waitTicks *= 2
		if j.waitTicks > xrayRestartBackoffMax {
			j.waitTicks = xrayRestartBackoffMax
		}
		logger.Warningf("xray is still down after a restart; next attempt in %ds", j.waitTicks)
	}
}
