package model

// NodeClientTraffic stores the last-seen absolute counters from a remote node
// for one client email. The sync job diffs against these baselines and adds
// only the delta into the master's xray.ClientTraffic rows.
type NodeClientTraffic struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId  int    `json:"nodeId" gorm:"uniqueIndex:idx_node_email,priority:1;index"`
	Email   string `json:"email" gorm:"uniqueIndex:idx_node_email,priority:2;size:255"`
	Up      int64  `json:"up" gorm:"default:0"`
	Down    int64  `json:"down" gorm:"default:0"`
	AllTime int64  `json:"allTime" gorm:"default:0"`
}
