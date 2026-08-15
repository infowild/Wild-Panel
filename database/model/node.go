package model

import "encoding/json"

// Node is a remote panel this master manages over its /panel/api surface.
// The master owns inbound/client definitions and mirrors them to the node;
// the node runs Xray. ApiToken is write-only on the wire (never returned).
type Node struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name    string `json:"name" form:"name" gorm:"uniqueIndex;size:64"`
	Remark  string `json:"remark" form:"remark" gorm:"size:255"`
	Scheme  string `json:"scheme" form:"scheme" gorm:"size:8;default:https"`
	Address string `json:"address" form:"address" gorm:"size:255;not null"`
	Port    int    `json:"port" form:"port" gorm:"default:2053"`
	// BasePath is the remote panel's web base path, always stored with a trailing slash.
	BasePath string `json:"basePath" form:"basePath" gorm:"size:255;default:/"`
	// ApiToken is the plaintext Bearer secret used to call the remote. Never JSON-exported.
	ApiToken string `json:"-" form:"apiToken" gorm:"size:255"`
	Enable   bool   `json:"enable" form:"enable" gorm:"default:1"`
	// AllowPrivateAddress bypasses the SSRF dialer block for loopback/RFC1918 targets.
	AllowPrivateAddress bool `json:"allowPrivateAddress" form:"allowPrivateAddress" gorm:"default:0"`

	// TlsVerifyMode: verify | skip | pin
	TlsVerifyMode    string `json:"tlsVerifyMode" form:"tlsVerifyMode" gorm:"size:16;default:verify"`
	PinnedCertSha256 string `json:"pinnedCertSha256" form:"pinnedCertSha256" gorm:"size:128"`

	// InboundSyncMode: all | selected
	InboundSyncMode string `json:"inboundSyncMode" form:"inboundSyncMode" gorm:"size:16;default:all"`
	// InboundTagsJSON stores the selected-mode tag allowlist as a JSON array.
	InboundTagsJSON string `json:"-" gorm:"column:inbound_tags;type:text"`

	// Runtime / health (written by heartbeat and sync jobs).
	Status            string  `json:"status" gorm:"size:16;default:unknown"`
	LastHeartbeat     int64   `json:"lastHeartbeat" gorm:"default:0"`
	LatencyMs         int     `json:"latencyMs" gorm:"default:0"`
	XrayVersion       string  `json:"xrayVersion" gorm:"size:64"`
	PanelVersion      string  `json:"panelVersion" gorm:"size:64"`
	Guid              string  `json:"guid" gorm:"size:64"`
	CpuPct            float64 `json:"cpuPct" gorm:"default:0"`
	MemPct            float64 `json:"memPct" gorm:"default:0"`
	UptimeSecs        uint64  `json:"uptimeSecs" gorm:"default:0"`
	NetUp             uint64  `json:"netUp" gorm:"default:0"`
	NetDown           uint64  `json:"netDown" gorm:"default:0"`
	LastError         string  `json:"lastError" gorm:"size:512"`
	XrayState         string  `json:"xrayState" gorm:"size:32"`
	XrayError         string  `json:"xrayError" gorm:"size:512"`
	ConfigDirty       bool    `json:"configDirty" gorm:"default:0"`
	ConfigDirtyAt     int64   `json:"configDirtyAt" gorm:"default:0"`
	InboundsAdoptedAt int64   `json:"-" gorm:"default:0"`

	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`

	// HasApiToken is set on views; never stored.
	HasApiToken bool `json:"hasApiToken" gorm:"-"`
	// InboundTags is the decoded allowlist for the API/UI.
	InboundTags []string `json:"inboundTags" form:"inboundTags" gorm:"-"`
	// InboundCount is how many local inbounds are assigned to this node.
	InboundCount int `json:"inboundCount" gorm:"-"`
}

// EncodeInboundTags writes InboundTags into InboundTagsJSON.
func (n *Node) EncodeInboundTags() {
	if len(n.InboundTags) == 0 {
		n.InboundTagsJSON = "[]"
		return
	}
	b, err := json.Marshal(n.InboundTags)
	if err != nil {
		n.InboundTagsJSON = "[]"
		return
	}
	n.InboundTagsJSON = string(b)
}

// DecodeInboundTags populates InboundTags from InboundTagsJSON.
func (n *Node) DecodeInboundTags() {
	n.InboundTags = nil
	if n.InboundTagsJSON == "" {
		return
	}
	_ = json.Unmarshal([]byte(n.InboundTagsJSON), &n.InboundTags)
}

// AllowsTag reports whether a local inbound tag should be mirrored to this node.
func (n *Node) AllowsTag(tag string) bool {
	if n.InboundSyncMode != "selected" {
		return true
	}
	n.DecodeInboundTags()
	for _, t := range n.InboundTags {
		if t == tag {
			return true
		}
	}
	return false
}
