package job

import (
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeSyncJob reconciles dirty nodes and pulls remote client traffic.
type NodeSyncJob struct {
	nodeService service.NodeService
}

func NewNodeSyncJob() *NodeSyncJob {
	return &NodeSyncJob{}
}

func (j *NodeSyncJob) Run() {
	dirty, err := j.nodeService.DirtyNodes()
	if err != nil {
		logger.Warning("node sync dirty list:", err)
	} else {
		for _, n := range dirty {
			if err := j.nodeService.ReconcileNode(n.Id); err != nil {
				logger.Warning("node", n.Id, "reconcile:", err)
			}
		}
	}

	nodes, err := j.nodeService.EnabledNodes()
	if err != nil {
		logger.Warning("node traffic list:", err)
		return
	}
	for _, n := range nodes {
		if err := j.nodeService.PullTraffic(n.Id); err != nil {
			logger.Debugf("node %d traffic: %v", n.Id, err)
		}
	}
}
