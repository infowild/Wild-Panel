package service

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

// NodeService manages remote panel nodes and inbound mirror sync.
type NodeService struct{}

// NodeSpec is the create/update payload from the controller.
type NodeSpec struct {
	Name                string
	Remark              string
	Scheme              string
	Address             string
	Port                int
	BasePath            string
	ApiToken            string // empty on update = leave unchanged
	Enable              bool
	AllowPrivateAddress bool
	TlsVerifyMode       string
	PinnedCertSha256    string
	InboundSyncMode     string
	InboundTags         []string
}

var (
	nodePushFP = map[int]map[string]string{} // nodeId -> tag -> fingerprint
	nodePushMu sync.Mutex
)

func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func normalizeNodeSpec(spec *NodeSpec) error {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return common.NewError("node name is required")
	}
	if len(spec.Name) > 64 {
		return common.NewError("node name must be 64 characters or fewer")
	}
	spec.Address = strings.TrimSpace(spec.Address)
	if spec.Address == "" {
		return common.NewError("node address is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return common.NewError("port must be 1-65535")
	}
	scheme := strings.ToLower(strings.TrimSpace(spec.Scheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	spec.Scheme = scheme
	spec.BasePath = normalizeBasePath(spec.BasePath)
	mode := strings.ToLower(strings.TrimSpace(spec.TlsVerifyMode))
	switch mode {
	case "verify", "skip", "pin":
		spec.TlsVerifyMode = mode
	default:
		spec.TlsVerifyMode = "verify"
	}
	syncMode := strings.ToLower(strings.TrimSpace(spec.InboundSyncMode))
	if syncMode != "selected" {
		syncMode = "all"
	}
	spec.InboundSyncMode = syncMode
	return nil
}

func nodeToView(n *model.Node) *model.Node {
	if n == nil {
		return nil
	}
	n.DecodeInboundTags()
	n.HasApiToken = strings.TrimSpace(n.ApiToken) != ""
	// Never leak the token on the wire.
	n.ApiToken = ""
	return n
}

// List returns all nodes with inbound counts.
func (s *NodeService) List() ([]*model.Node, error) {
	db := database.GetDB()
	var rows []*model.Node
	if err := db.Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	type countRow struct {
		NodeId int
		Cnt    int
	}
	var counts []countRow
	_ = db.Model(&model.Inbound{}).
		Select("node_id as node_id, count(*) as cnt").
		Where("node_id > 0").
		Group("node_id").
		Scan(&counts)
	cmap := map[int]int{}
	for _, c := range counts {
		cmap[c.NodeId] = c.Cnt
	}
	out := make([]*model.Node, 0, len(rows))
	for _, n := range rows {
		n.InboundCount = cmap[n.Id]
		out = append(out, nodeToView(n))
	}
	return out, nil
}

// Get returns one node by id (token redacted).
func (s *NodeService) Get(id int) (*model.Node, error) {
	db := database.GetDB()
	var n model.Node
	if err := db.First(&n, id).Error; err != nil {
		return nil, err
	}
	var cnt int64
	_ = db.Model(&model.Inbound{}).Where("node_id = ?", id).Count(&cnt).Error
	n.InboundCount = int(cnt)
	return nodeToView(&n), nil
}

func (s *NodeService) loadRaw(id int) (*model.Node, error) {
	db := database.GetDB()
	var n model.Node
	if err := db.First(&n, id).Error; err != nil {
		return nil, err
	}
	n.DecodeInboundTags()
	return &n, nil
}

// Add creates a node and marks it dirty so the first sync runs.
func (s *NodeService) Add(spec NodeSpec) (*model.Node, error) {
	if err := normalizeNodeSpec(&spec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.ApiToken) == "" {
		return nil, common.NewError("api token is required")
	}
	db := database.GetDB()
	var count int64
	if err := db.Model(&model.Node{}).Where("name = ?", spec.Name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, common.NewError("a node with that name already exists")
	}
	n := &model.Node{
		Name:                spec.Name,
		Remark:              spec.Remark,
		Scheme:              spec.Scheme,
		Address:             spec.Address,
		Port:                spec.Port,
		BasePath:            spec.BasePath,
		ApiToken:            strings.TrimSpace(spec.ApiToken),
		Enable:              spec.Enable,
		AllowPrivateAddress: spec.AllowPrivateAddress,
		TlsVerifyMode:       spec.TlsVerifyMode,
		PinnedCertSha256:    strings.TrimSpace(spec.PinnedCertSha256),
		InboundSyncMode:     spec.InboundSyncMode,
		InboundTags:         spec.InboundTags,
		Status:              "unknown",
		ConfigDirty:         true,
		ConfigDirtyAt:       time.Now().Unix(),
	}
	n.EncodeInboundTags()
	if err := db.Create(n).Error; err != nil {
		return nil, err
	}
	return nodeToView(n), nil
}

// Update edits a node. Empty ApiToken leaves the stored token unchanged.
func (s *NodeService) Update(id int, spec NodeSpec) (*model.Node, error) {
	if err := normalizeNodeSpec(&spec); err != nil {
		return nil, err
	}
	db := database.GetDB()
	var n model.Node
	if err := db.First(&n, id).Error; err != nil {
		return nil, err
	}
	var count int64
	if err := db.Model(&model.Node{}).Where("name = ? AND id <> ?", spec.Name, id).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, common.NewError("a node with that name already exists")
	}
	n.Name = spec.Name
	n.Remark = spec.Remark
	n.Scheme = spec.Scheme
	n.Address = spec.Address
	n.Port = spec.Port
	n.BasePath = spec.BasePath
	n.Enable = spec.Enable
	n.AllowPrivateAddress = spec.AllowPrivateAddress
	n.TlsVerifyMode = spec.TlsVerifyMode
	n.PinnedCertSha256 = strings.TrimSpace(spec.PinnedCertSha256)
	n.InboundSyncMode = spec.InboundSyncMode
	n.InboundTags = spec.InboundTags
	n.EncodeInboundTags()
	if tok := strings.TrimSpace(spec.ApiToken); tok != "" {
		n.ApiToken = tok
	}
	n.ConfigDirty = true
	n.ConfigDirtyAt = time.Now().Unix()
	if err := db.Save(&n).Error; err != nil {
		return nil, err
	}
	return nodeToView(&n), nil
}

// Delete removes a node and clears NodeId on its assigned inbounds (they become local).
func (s *NodeService) Delete(id int) error {
	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Inbound{}).Where("node_id = ?", id).Update("node_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.NodeClientTraffic{}).Error; err != nil {
			return err
		}
		res := tx.Delete(&model.Node{}, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		nodePushMu.Lock()
		delete(nodePushFP, id)
		nodePushMu.Unlock()
		return nil
	})
}

// SetEnable toggles whether heartbeat/sync run for this node.
func (s *NodeService) SetEnable(id int, enable bool) error {
	db := database.GetDB()
	res := db.Model(&model.Node{}).Where("id = ?", id).Update("enable", enable)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	if enable {
		return s.MarkDirty(id)
	}
	return nil
}

// MarkDirty flags a node for reconcile. Safe with id<=0 (no-op).
func (s *NodeService) MarkDirty(nodeId int) error {
	if nodeId <= 0 {
		return nil
	}
	db := database.GetDB()
	return db.Model(&model.Node{}).Where("id = ?", nodeId).Updates(map[string]any{
		"config_dirty":    true,
		"config_dirty_at": time.Now().Unix(),
	}).Error
}

// MarkDirtyForInbound marks the inbound's node dirty when NodeId > 0.
func MarkDirtyForInbound(inbound *model.Inbound) {
	if inbound == nil || inbound.NodeId <= 0 {
		return
	}
	if err := (&NodeService{}).MarkDirty(inbound.NodeId); err != nil {
		logger.Warning("mark node dirty:", err)
	}
}

// ClearDirty clears the dirty flag after a successful reconcile.
func (s *NodeService) ClearDirty(nodeId int) error {
	db := database.GetDB()
	return db.Model(&model.Node{}).Where("id = ?", nodeId).Updates(map[string]any{
		"config_dirty":    false,
		"config_dirty_at": 0,
	}).Error
}

func (s *NodeService) remoteFor(n *model.Node) (*RemoteNode, error) {
	return NewRemoteNode(n, n.ApiToken)
}

// applyStatus writes heartbeat fields onto n (caller saves).
func applyStatus(n *model.Node, st *RemoteStatus, latencyMs int, err error) {
	n.LastHeartbeat = time.Now().Unix()
	n.LatencyMs = latencyMs
	if err != nil {
		n.Status = "offline"
		n.LastError = truncateErr(err.Error(), 500)
		return
	}
	n.Status = "online"
	n.LastError = ""
	if st == nil {
		return
	}
	n.CpuPct = st.Cpu
	if st.Mem.Total > 0 {
		n.MemPct = float64(st.Mem.Current) / float64(st.Mem.Total) * 100
	}
	n.UptimeSecs = st.Uptime
	n.NetUp = st.NetIO.Up
	n.NetDown = st.NetIO.Down
	n.XrayVersion = st.Xray.Version
	n.XrayState = st.Xray.State
	n.XrayError = st.Xray.ErrorMsg
	if st.PanelVersion != "" {
		n.PanelVersion = st.PanelVersion
	}
	if st.PanelGuid != "" {
		n.Guid = st.PanelGuid
	}
}

func truncateErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Probe hits the remote status endpoint and persists health fields.
func (s *NodeService) Probe(id int) (*model.Node, error) {
	n, err := s.loadRaw(id)
	if err != nil {
		return nil, err
	}
	remote, err := s.remoteFor(n)
	if err != nil {
		applyStatus(n, nil, 0, err)
		_ = database.GetDB().Save(n).Error
		return nodeToView(n), err
	}
	st, latency, err := remote.Status()
	applyStatus(n, st, int(latency.Milliseconds()), err)
	if saveErr := database.GetDB().Save(n).Error; saveErr != nil {
		return nodeToView(n), saveErr
	}
	if err != nil {
		return nodeToView(n), err
	}
	return nodeToView(n), nil
}

// TestConnection probes a not-yet-saved (or edited) connection without persisting.
func (s *NodeService) TestConnection(spec NodeSpec) (*RemoteStatus, int, error) {
	if err := normalizeNodeSpec(&spec); err != nil {
		return nil, 0, err
	}
	token := strings.TrimSpace(spec.ApiToken)
	if token == "" {
		return nil, 0, common.NewError("api token is required to test")
	}
	n := &model.Node{
		Scheme:              spec.Scheme,
		Address:             spec.Address,
		Port:                spec.Port,
		BasePath:            spec.BasePath,
		AllowPrivateAddress: spec.AllowPrivateAddress,
		TlsVerifyMode:       spec.TlsVerifyMode,
		PinnedCertSha256:    spec.PinnedCertSha256,
	}
	remote, err := NewRemoteNode(n, token)
	if err != nil {
		return nil, 0, err
	}
	st, latency, err := remote.Status()
	return st, int(latency.Milliseconds()), err
}

// StoredToken returns the saved plaintext api token for a node. Used so the edit
// dialog can re-test a connection without the operator retyping the secret (the
// token field is intentionally blank on edit and never echoed back to the UI).
func (s *NodeService) StoredToken(id int) (string, error) {
	n, err := s.loadRaw(id)
	if err != nil {
		return "", err
	}
	return n.ApiToken, nil
}

// CertFingerprint fetches the remote leaf cert SHA-256 (base64).
func (s *NodeService) CertFingerprint(spec NodeSpec) (string, error) {
	if err := normalizeNodeSpec(&spec); err != nil {
		return "", err
	}
	n := &model.Node{
		Scheme:              spec.Scheme,
		Address:             spec.Address,
		Port:                spec.Port,
		BasePath:            spec.BasePath,
		AllowPrivateAddress: spec.AllowPrivateAddress,
	}
	return FetchCertFingerprint(n)
}

// FetchRemoteInbounds lists inbounds on a saved or ad-hoc connection.
func (s *NodeService) FetchRemoteInbounds(id int, spec *NodeSpec) ([]RemoteInbound, error) {
	var n *model.Node
	var token string
	var err error
	if id > 0 {
		n, err = s.loadRaw(id)
		if err != nil {
			return nil, err
		}
		token = n.ApiToken
	} else if spec != nil {
		if err := normalizeNodeSpec(spec); err != nil {
			return nil, err
		}
		token = strings.TrimSpace(spec.ApiToken)
		n = &model.Node{
			Scheme:              spec.Scheme,
			Address:             spec.Address,
			Port:                spec.Port,
			BasePath:            spec.BasePath,
			AllowPrivateAddress: spec.AllowPrivateAddress,
			TlsVerifyMode:       spec.TlsVerifyMode,
			PinnedCertSha256:    spec.PinnedCertSha256,
		}
	} else {
		return nil, common.NewError("node id or connection details required")
	}
	remote, err := NewRemoteNode(n, token)
	if err != nil {
		return nil, err
	}
	return remote.ListInbounds()
}

// ReconcileNode pushes local node-assigned inbounds to the remote and optionally
// deletes remote tags that are no longer desired after the first successful adopt.
func (s *NodeService) ReconcileNode(id int) error {
	n, err := s.loadRaw(id)
	if err != nil {
		return err
	}
	if !n.Enable {
		return nil
	}
	remote, err := s.remoteFor(n)
	if err != nil {
		return err
	}
	remoteRows, err := remote.ListInbounds()
	if err != nil {
		return err
	}
	byTag := map[string]RemoteInbound{}
	for _, r := range remoteRows {
		byTag[r.Tag] = r
	}

	db := database.GetDB()
	var locals []*model.Inbound
	if err := db.Where("node_id = ?", id).Find(&locals).Error; err != nil {
		return err
	}

	desired := map[string]bool{}
	nodePushMu.Lock()
	if nodePushFP[id] == nil {
		nodePushFP[id] = map[string]string{}
	}
	fps := nodePushFP[id]
	nodePushMu.Unlock()

	for _, in := range locals {
		if !n.AllowsTag(in.Tag) {
			continue
		}
		desired[in.Tag] = true
		form := WireInboundForm(in)
		fp := FormFingerprint(form)
		nodePushMu.Lock()
		prev := fps[in.Tag]
		nodePushMu.Unlock()
		if remoteIn, ok := byTag[in.Tag]; ok {
			if prev == fp {
				continue
			}
			if err := remote.UpdateInbound(remoteIn.Id, form); err != nil {
				return fmt.Errorf("update %s: %w", in.Tag, err)
			}
		} else {
			created, err := remote.AddInbound(form)
			if err != nil {
				return fmt.Errorf("add %s: %w", in.Tag, err)
			}
			if created != nil && created.Tag != "" {
				byTag[created.Tag] = *created
			} else {
				byTag[in.Tag] = RemoteInbound{Tag: in.Tag}
			}
		}
		nodePushMu.Lock()
		fps[in.Tag] = fp
		nodePushMu.Unlock()
	}

	// Sweep extras only after the first clean adopt so we never wipe a fresh node
	// before its first successful push.
	if n.InboundsAdoptedAt != 0 {
		for tag, remoteIn := range byTag {
			if desired[tag] {
				continue
			}
			if n.InboundSyncMode == "selected" && !n.AllowsTag(tag) {
				// Selected mode: do not delete tags outside the allowlist.
				continue
			}
			if err := remote.DelInbound(remoteIn.Id); err != nil {
				logger.Warning("node", id, "delete remote tag", tag, ":", err)
			} else {
				nodePushMu.Lock()
				delete(fps, tag)
				nodePushMu.Unlock()
			}
		}
	}

	now := time.Now().Unix()
	updates := map[string]any{
		"config_dirty":    false,
		"config_dirty_at": 0,
	}
	if n.InboundsAdoptedAt == 0 {
		updates["inbounds_adopted_at"] = now
	}
	return db.Model(&model.Node{}).Where("id = ?", id).Updates(updates).Error
}

// PullTraffic merges remote client counter deltas into the master's ClientTraffic.
func (s *NodeService) PullTraffic(id int) error {
	n, err := s.loadRaw(id)
	if err != nil {
		return err
	}
	if !n.Enable || n.Status == "offline" {
		return nil
	}
	remote, err := s.remoteFor(n)
	if err != nil {
		return err
	}
	rows, err := remote.ListInbounds()
	if err != nil {
		return err
	}
	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		for _, ri := range rows {
			stats := ParseRemoteClientStats(ri.ClientStats)
			for _, st := range stats {
				email := strings.TrimSpace(st.Email)
				if email == "" {
					continue
				}
				if err := mergeClientDelta(tx, id, email, st.Up, st.Down, st.AllTime); err != nil {
					return err
				}
			}
			// Also fold inbound-level counters when present (no per-client stats).
			if len(stats) == 0 && (ri.Up > 0 || ri.Down > 0) {
				var local model.Inbound
				if err := tx.Where("node_id = ? AND tag = ?", id, ri.Tag).First(&local).Error; err != nil {
					continue
				}
				// Store absolute remote values on the local inbound traffic columns
				// for display; client-level aggregation is the primary path.
				_ = tx.Model(&local).Updates(map[string]any{
					"up":   ri.Up,
					"down": ri.Down,
				}).Error
			}
		}
		return nil
	})
}

func mergeClientDelta(tx *gorm.DB, nodeId int, email string, up, down, allTime int64) error {
	var base model.NodeClientTraffic
	err := tx.Where("node_id = ? AND email = ?", nodeId, email).First(&base).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	newRow := err == gorm.ErrRecordNotFound

	// First time we ever see this (node,email): SEED the baseline to the node's
	// current absolute counters and add nothing to the master. Master usage is
	// counted from the moment the node is under management, so pre-existing usage
	// on a freshly connected node — or a node deleted and re-added — can never be
	// imported or double-counted. This is the invariant the operator asked for:
	// the master's stored consumption only ever grows from real, post-connect use
	// and is never reset or rewritten by anything the remote (reseller) panel does.
	if newRow {
		seed := model.NodeClientTraffic{
			NodeId:  nodeId,
			Email:   email,
			Up:      up,
			Down:    down,
			AllTime: allTime,
		}
		return tx.Create(&seed).Error
	}

	dUp := up - base.Up
	dDown := down - base.Down
	dAll := allTime - base.AllTime
	// A remote reset (Up/Down fall back to 0) or any counter going backwards must
	// never subtract from the master. Clamp every direction at zero: the master's
	// consumption is monotonic and immune to remote resets.
	if dUp < 0 {
		dUp = 0
	}
	if dDown < 0 {
		dDown = 0
	}
	if dAll < 0 {
		dAll = 0
	}

	if dUp > 0 || dDown > 0 || dAll > 0 {
		var ct xray.ClientTraffic
		if err := tx.Where("email = ?", email).First(&ct).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			// No local client with this email — still update baseline so we do not
			// replay a huge delta if the account is added later.
		} else {
			usageDelta := dUp + dDown
			updates := map[string]any{
				"up":   ct.Up + dUp,
				"down": ct.Down + dDown,
			}
			if dAll > 0 {
				updates["all_time"] = ct.AllTime + dAll
				usageDelta = dAll
			} else if dUp+dDown > 0 {
				updates["all_time"] = ct.AllTime + dUp + dDown
			}
			if err := tx.Model(&ct).Updates(updates).Error; err != nil {
				return err
			}
			if err := addResellerUsage(tx, map[string]int64{email: usageDelta}); err != nil {
				return err
			}
			// Keep the parent inbound totals in step for the overview cards.
			_ = tx.Model(&model.Inbound{}).Where("id = ?", ct.InboundId).
				UpdateColumn("up", gorm.Expr("up + ?", dUp)).
				UpdateColumn("down", gorm.Expr("down + ?", dDown)).Error
		}
	}

	// Advance the baseline to the node's current absolute counters so the next
	// pull measures the next delta. After a remote reset this stores the lower
	// value, so subsequent growth is counted from the reset point forward without
	// ever rewinding what the master already recorded.
	base.Up = up
	base.Down = down
	base.AllTime = allTime
	return tx.Save(&base).Error
}

// EnabledNodes returns enabled nodes for jobs.
func (s *NodeService) EnabledNodes() ([]*model.Node, error) {
	db := database.GetDB()
	var rows []*model.Node
	if err := db.Where("enable = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DirtyNodes returns enabled nodes with ConfigDirty set.
func (s *NodeService) DirtyNodes() ([]*model.Node, error) {
	db := database.GetDB()
	var rows []*model.Node
	if err := db.Where("enable = ? AND config_dirty = ?", true, true).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
