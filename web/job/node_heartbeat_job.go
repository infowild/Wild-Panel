package job

import (
	"sync"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeHeartbeatJob probes every enabled node and persists health fields.
type NodeHeartbeatJob struct {
	nodeService service.NodeService
}

func NewNodeHeartbeatJob() *NodeHeartbeatJob {
	return &NodeHeartbeatJob{}
}

func (j *NodeHeartbeatJob) Run() {
	nodes, err := j.nodeService.EnabledNodes()
	if err != nil {
		logger.Warning("node heartbeat list:", err)
		return
	}
	if len(nodes) == 0 {
		return
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := j.nodeService.Probe(id); err != nil {
				logger.Debugf("node %d heartbeat: %v", id, err)
			}
		}(n.Id)
	}
	wg.Wait()
}
