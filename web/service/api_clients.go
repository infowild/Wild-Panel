package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/util/random"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// ApiClientService exposes email-keyed client ops used by mirzabot
// (/panel/api/clients/*). It builds on InboundService so every protocol's
// existing add/update/delete/reset path (slots, RADIUS, keys, uniqueness)
// stays the single source of truth.
type ApiClientService struct {
	Inbound  InboundService
	Mtproto  MtprotoService
	Setting  SettingService
	SubLinksBuilder func(host, subId string) ([]string, error)
}

// ClientCreatePayload matches mirzabot / Sanaei POST /panel/api/clients/add.
type ClientCreatePayload struct {
	Client     model.Client `json:"client"`
	InboundIds []int        `json:"inboundIds"`
}

// ClientFlatView is the flat shape mirzabot reads from GET /clients/get/:email.
type ClientFlatView struct {
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	TgId       int64  `json:"tgId"`
	LimitIp    int    `json:"limitIp"`
	SubId      string `json:"subId"`
	Comment    string `json:"comment"`
	InboundIds []int  `json:"inboundIds"`
}

// ClientSearchItem is one row in GET /clients/search.
type ClientSearchItem struct {
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	SubId      string `json:"subId"`
	InboundId  int    `json:"inboundId"`
}

// ClientSearchResult is the obj payload for /clients/search.
type ClientSearchResult struct {
	Items []ClientSearchItem `json:"items"`
	Total int                `json:"total"`
}

// GetFlat returns mirzabot-compatible client metadata by email.
func (s *ApiClientService) GetFlat(email string) (*ClientFlatView, error) {
	email = strings.TrimSpace(email)
	traffic, client, err := s.Inbound.GetClientByEmail(email)
	if err != nil {
		return nil, err
	}
	inboundId := 0
	if traffic != nil {
		inboundId = traffic.InboundId
	}
	total := client.TotalGB
	expiry := client.ExpiryTime
	if traffic != nil {
		if traffic.Total > 0 {
			total = traffic.Total
		}
		if traffic.ExpiryTime != 0 {
			expiry = traffic.ExpiryTime
		}
	}
	return &ClientFlatView{
		Email:      client.Email,
		Enable:     client.Enable,
		TotalGB:    total,
		ExpiryTime: expiry,
		TgId:       client.TgID,
		LimitIp:    client.LimitIP,
		SubId:      client.SubID,
		Comment:    client.Comment,
		InboundIds: []int{inboundId},
	}, nil
}

// GetTraffic returns ClientTraffic for email (mirzabot /clients/traffic/:email).
func (s *ApiClientService) GetTraffic(email string) (*xray.ClientTraffic, error) {
	return s.Inbound.GetClientTrafficByEmail(strings.TrimSpace(email))
}

// Search lists clients whose email contains the search string (case-insensitive).
func (s *ApiClientService) Search(search string, page, size int) (*ClientSearchResult, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 25
	}
	if size > 200 {
		size = 200
	}
	search = strings.ToLower(strings.TrimSpace(search))

	inbounds, err := s.Inbound.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	items := make([]ClientSearchItem, 0)
	for _, ib := range inbounds {
		if ib.Protocol == model.WireGuard {
			continue // peers, not clients[]
		}
		clients, err := s.Inbound.GetClients(ib)
		if err != nil {
			continue
		}
		for _, c := range clients {
			if search != "" && !strings.Contains(strings.ToLower(c.Email), search) {
				continue
			}
			items = append(items, ClientSearchItem{
				Email:      c.Email,
				Enable:     c.Enable,
				TotalGB:    c.TotalGB,
				ExpiryTime: c.ExpiryTime,
				SubId:      c.SubID,
				InboundId:  ib.Id,
			})
		}
	}
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return &ClientSearchResult{Items: items[start:end], Total: total}, nil
}

// Create adds a client to the first inbound in inboundIds. Extra ids are ignored
// because this panel keeps email unique panel-wide (one traffic row per email).
// Credentials are minted per-protocol so every existing protocol works.
func (s *ApiClientService) Create(payload *ClientCreatePayload) (model.Protocol, bool, error) {
	if payload == nil {
		return "", false, common.NewError("empty payload")
	}
	email := strings.TrimSpace(payload.Client.Email)
	if email == "" {
		return "", false, common.NewError("client email is required")
	}
	if len(payload.InboundIds) == 0 {
		return "", false, common.NewError("at least one inbound is required")
	}
	inboundId := payload.InboundIds[0]
	ib, err := s.Inbound.GetInbound(inboundId)
	if err != nil {
		return "", false, err
	}
	if ib.Protocol == model.WireGuard {
		return "", false, common.NewError("Xray WireGuard inbounds use peers, not clients; pick another protocol")
	}

	base := payload.Client
	base.Email = email
	if base.SubID == "" {
		base.SubID = strings.ToLower(random.Seq(16))
	}
	// New API accounts start enabled (mirzabot always wants a live account).
	base.Enable = true

	client, err := s.mintClient(ib, base)
	if err != nil {
		return ib.Protocol, false, err
	}

	settingsBytes, err := json.Marshal(map[string][]model.Client{"clients": {client}})
	if err != nil {
		return ib.Protocol, false, err
	}
	needRestart, err := s.Inbound.AddInboundClient(&model.Inbound{
		Id:       inboundId,
		Settings: string(settingsBytes),
	})
	return ib.Protocol, needRestart, err
}

// Update patches quota/meta fields on an existing client by email (credentials preserved).
func (s *ApiClientService) Update(email string, patch map[string]any) (model.Protocol, bool, error) {
	email = strings.TrimSpace(email)
	_, existing, err := s.Inbound.GetClientByEmail(email)
	if err != nil {
		return "", false, err
	}
	traffic, ib, err := s.Inbound.GetClientInboundByEmail(email)
	if err != nil || ib == nil {
		return "", false, common.NewError("inbound not found for email:", email)
	}
	_ = traffic

	updated := *existing
	if v, ok := patch["totalGB"]; ok {
		updated.TotalGB = toInt64(v)
	}
	if v, ok := patch["expiryTime"]; ok {
		updated.ExpiryTime = toInt64(v)
	}
	if v, ok := patch["enable"]; ok {
		updated.Enable = toBool(v)
	}
	if v, ok := patch["tgId"]; ok {
		updated.TgID = toInt64(v)
	}
	if v, ok := patch["limitIp"]; ok {
		updated.LimitIP = int(toInt64(v))
	}
	if v, ok := patch["subId"]; ok {
		if s, ok := v.(string); ok {
			updated.SubID = s
		}
	}
	if v, ok := patch["comment"]; ok {
		if s, ok := v.(string); ok {
			updated.Comment = s
		}
	}
	updated.Email = email

	clientId := clientIdentity(ib.Protocol, *existing)
	settingsBytes, err := json.Marshal(map[string][]model.Client{"clients": {updated}})
	if err != nil {
		return ib.Protocol, false, err
	}
	needRestart, err := s.Inbound.UpdateInboundClient(&model.Inbound{
		Id:       ib.Id,
		Settings: string(settingsBytes),
	}, clientId)
	return ib.Protocol, needRestart, err
}

// Delete removes the client by email across its inbound.
func (s *ApiClientService) Delete(email string, keepTraffic bool) (model.Protocol, bool, error) {
	email = strings.TrimSpace(email)
	traffic, ib, err := s.Inbound.GetClientInboundByEmail(email)
	if err != nil || ib == nil {
		return "", false, common.NewError("User not found")
	}
	_ = traffic
	needRestart, err := s.Inbound.DelInboundClientByEmail(ib.Id, email)
	if err != nil {
		return ib.Protocol, false, err
	}
	if !keepTraffic {
		// DelInboundClientByEmail already drops traffic in normal path; keepTraffic
		// reserved for Sanaei parity (ignored when already deleted with the client).
	}
	return ib.Protocol, needRestart, nil
}

// ResetTraffic zeroes counters for email.
func (s *ApiClientService) ResetTraffic(email string) error {
	return s.Inbound.ResetClientTrafficByEmail(strings.TrimSpace(email))
}

// GetSubLinks returns connection / subscription URLs for a subId.
func (s *ApiClientService) GetSubLinks(host, subId string) ([]string, error) {
	subId = strings.TrimSpace(subId)
	if subId == "" {
		return nil, common.NewError("subId is required")
	}
	if s.SubLinksBuilder != nil {
		return s.SubLinksBuilder(host, subId)
	}
	return []string{}, nil
}

// mintClient fills protocol-specific credentials on top of buildTargetClientFromSource.
func (s *ApiClientService) mintClient(ib *model.Inbound, base model.Client) (model.Client, error) {
	c, err := s.Inbound.buildTargetClientFromSource(base, ib.Protocol, base.Email, base.Flow)
	if err != nil {
		return c, err
	}
	// Preserve quota/meta from the API payload (buildTarget copies source which has them).
	c.TotalGB = base.TotalGB
	c.ExpiryTime = base.ExpiryTime
	c.Enable = base.Enable
	c.TgID = base.TgID
	c.LimitIP = base.LimitIP
	c.SubID = base.SubID
	c.Comment = base.Comment
	c.Email = base.Email

	switch ib.Protocol {
	case model.SSH:
		if c.Password == "" {
			c.Password = strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		}
		if c.ID == "" {
			c.ID = base.Email
		}
	case model.MTPROTO:
		if c.Secret == "" {
			sec, err := s.Mtproto.GenerateSecret()
			if err != nil {
				return c, err
			}
			c.Secret = sec
		}
		c.ID = base.Email
		if !c.ModeClassic && !c.ModeSecure && !c.ModeTls {
			c.ModeClassic = true
			c.ModeSecure = true
			c.ModeTls = true
		}
	case model.WGC, model.AWG, model.GRE:
		c.ID = base.Email
	case model.Shadowsocks:
		if c.Password == "" {
			c.Password = s.Inbound.generateRandomCredential(ib.Protocol)
		}
		c.Security = "auto"
	}

	if clientIdentity(ib.Protocol, c) == "" {
		return c, common.NewError("cannot mint client for protocol ", ib.Protocol)
	}
	return c, nil
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	case string:
		var n int64
		fmt.Sscan(t, &n)
		return n
	default:
		return 0
	}
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}
