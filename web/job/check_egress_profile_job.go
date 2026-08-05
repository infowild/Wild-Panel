package job

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// CheckEgressProfileJob warns when the international egress profile is enabled
// but its outbound tunnel (VPN client / SSH) is down.
type CheckEgressProfileJob struct {
	egressService      service.EgressProfileService
	vpnOutboundService service.VpnOutboundService
	sshOutboundService service.SshOutboundService
	downStreak         int
}

// NewCheckEgressProfileJob creates the egress profile health job.
func NewCheckEgressProfileJob() *CheckEgressProfileJob {
	return &CheckEgressProfileJob{}
}

// Run logs a warning when the configured egress outbound is not reachable.
func (j *CheckEgressProfileJob) Run() {
	profile, err := j.egressService.Get()
	if err != nil || !profile.Enabled {
		j.downStreak = 0
		return
	}
	tag := strings.TrimSpace(profile.OutboundTag)
	if tag == "" {
		return
	}

	up := j.egressOutboundUp(tag)
	if up {
		j.downStreak = 0
		return
	}
	j.downStreak++
	if j.downStreak < 2 {
		return
	}
	j.downStreak = 0
	logger.Warning("egress profile: outbound", tag, "is down — clients routed through it may have no international access")
}

func (j *CheckEgressProfileJob) egressOutboundUp(tag string) bool {
	for _, t := range j.vpnOutboundService.List() {
		if t.Tag != tag || !t.Enable {
			continue
		}
		running, _ := j.vpnOutboundService.Status(tag)
		return running
	}
	for _, t := range j.sshOutboundService.List() {
		if t.Tag != tag || !t.Enable {
			continue
		}
		running, _ := j.sshOutboundService.Status(tag)
		return running
	}
	// Not a panel-managed tunnel — assume up (freedom/socks/warp in template).
	return true
}
