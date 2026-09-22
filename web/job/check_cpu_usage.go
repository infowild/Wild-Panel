package job

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/shirou/gopsutil/v4/cpu"
)

// cpuSampleWindow is how long one measurement averages over, and it must stay well
// under the job's own 10s schedule (see web.go).
//
// It used to be a full minute while the job ran every 10 seconds. cron starts each
// run in its own goroutine and never waits for the previous one, so six overlapping
// samplers were permanently in flight, each holding a goroutine and re-reading
// /proc/stat, and a single threshold breach fanned out into six identical Telegram
// alerts a minute. The window now fits inside one tick, and cpuJobRunning makes the
// overlap impossible even if the schedule is changed again.
const cpuSampleWindow = 5 * time.Second

// cpuJobRunning is the single-flight guard for the sampler above. Package-level
// because cron hands each run a fresh job value.
var cpuJobRunning atomic.Bool

// CheckCpuJob monitors CPU usage and sends Telegram notifications when usage exceeds the configured threshold.
type CheckCpuJob struct {
	tgbotService   service.Tgbot
	settingService service.SettingService
}

// NewCheckCpuJob creates a new CPU monitoring job instance.
func NewCheckCpuJob() *CheckCpuJob {
	return new(CheckCpuJob)
}

// Run checks CPU usage over the last minute and sends a Telegram alert if it exceeds the threshold.
func (j *CheckCpuJob) Run() {
	// Single-flight: the sample below blocks for its whole window, and cron would
	// otherwise stack a new sampler on top every tick.
	if !cpuJobRunning.CompareAndSwap(false, true) {
		return
	}
	defer cpuJobRunning.Store(false)

	threshold, err := j.settingService.GetTgCpu()
	if err != nil || threshold <= 0 {
		// If threshold cannot be retrieved or is not set, skip sending notifications
		return
	}

	// get latest status of server
	percent, err := cpu.Percent(cpuSampleWindow, false)
	if err == nil && len(percent) > 0 && percent[0] > float64(threshold) {
		msg := j.tgbotService.I18nBot("tgbot.messages.cpuThreshold",
			"Percent=="+strconv.FormatFloat(percent[0], 'f', 2, 64),
			"Threshold=="+strconv.Itoa(threshold))

		j.tgbotService.SendMsgToTgbotAdmins(msg)
	}
}
