package service

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

const (
	settingEgressProfileEnabled     = "egressProfileEnabled"
	settingEgressProfileOutboundTag = "egressProfileOutboundTag"
	settingEgressProfileIranDirect  = "egressProfileIranDirect"
	settingEgressProfileDns         = "egressProfileDns"
	settingEgressProfileDnsServers  = "egressProfileDnsServers"
)

// DefaultEgressProfileDNSServers are used when DNS override is on and no custom list is set.
var DefaultEgressProfileDNSServers = []string{"1.1.1.1", "8.8.8.8"}

// EgressProfile is the operator-facing "blackout / international egress" switch.
// It injects routing (and optionally DNS) at config-build time without rewriting
// the stored Xray template, so it can be toggled off cleanly.
type EgressProfile struct {
	Enabled     bool     `json:"enabled" form:"enabled"`
	OutboundTag string   `json:"outboundTag" form:"outboundTag"`
	IranDirect  bool     `json:"iranDirect" form:"iranDirect"`
	DnsEnabled  bool     `json:"dnsEnabled" form:"dnsEnabled"`
	DnsServers  []string `json:"dnsServers" form:"dnsServers"`
}

// EgressProfileService reads and applies the egress profile settings.
type EgressProfileService struct {
	SettingService SettingService
}

// Get returns the stored egress profile.
func (s *EgressProfileService) Get() (EgressProfile, error) {
	enabled, err := s.SettingService.getBool(settingEgressProfileEnabled)
	if err != nil {
		return EgressProfile{}, err
	}
	tag, err := s.SettingService.getString(settingEgressProfileOutboundTag)
	if err != nil {
		return EgressProfile{}, err
	}
	iranDirect, err := s.SettingService.getBool(settingEgressProfileIranDirect)
	if err != nil {
		return EgressProfile{}, err
	}
	dnsEnabled, err := s.SettingService.getBool(settingEgressProfileDns)
	if err != nil {
		return EgressProfile{}, err
	}
	serversRaw, err := s.SettingService.getString(settingEgressProfileDnsServers)
	if err != nil {
		return EgressProfile{}, err
	}
	servers := parseDNSServerList(serversRaw)
	if len(servers) == 0 {
		servers = append([]string(nil), DefaultEgressProfileDNSServers...)
	}
	return EgressProfile{
		Enabled:     enabled,
		OutboundTag: strings.TrimSpace(tag),
		IranDirect:  iranDirect,
		DnsEnabled:  dnsEnabled,
		DnsServers:  servers,
	}, nil
}

// Save persists the egress profile and flags Xray for a debounced restart.
func (s *EgressProfileService) Save(p EgressProfile) error {
	if err := s.SettingService.setBool(settingEgressProfileEnabled, p.Enabled); err != nil {
		return err
	}
	if err := s.SettingService.saveSetting(settingEgressProfileOutboundTag, strings.TrimSpace(p.OutboundTag)); err != nil {
		return err
	}
	if err := s.SettingService.setBool(settingEgressProfileIranDirect, p.IranDirect); err != nil {
		return err
	}
	if err := s.SettingService.setBool(settingEgressProfileDns, p.DnsEnabled); err != nil {
		return err
	}
	servers := p.DnsServers
	if len(servers) == 0 {
		servers = DefaultEgressProfileDNSServers
	}
	if err := s.SettingService.saveSetting(settingEgressProfileDnsServers, strings.Join(servers, ",")); err != nil {
		return err
	}
	(&XrayService{}).SetToNeedRestart()
	return nil
}

// Apply mutates the generated Xray config when the profile is enabled.
func ApplyEgressProfile(config *xray.Config, profile EgressProfile, inboundTags []string) {
	if config == nil || !profile.Enabled {
		return
	}
	tag := strings.TrimSpace(profile.OutboundTag)
	if tag == "" {
		logger.Warning("egress profile: enabled but outbound tag is empty, skipping")
		return
	}
	if profile.DnsEnabled {
		applyEgressProfileDNS(config, profile.DnsServers)
	}
	applyEgressProfileRouting(config, profile, inboundTags)
}

func applyEgressProfileDNS(config *xray.Config, servers []string) {
	if len(servers) == 0 {
		return
	}
	var dns map[string]any
	if len(config.DNSConfig) > 0 {
		if err := json.Unmarshal(config.DNSConfig, &dns); err != nil {
			dns = map[string]any{}
		}
	} else {
		dns = map[string]any{}
	}
	serverList := make([]any, 0, len(servers))
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s != "" {
			serverList = append(serverList, s)
		}
	}
	if len(serverList) == 0 {
		return
	}
	dns["servers"] = serverList
	dns["queryStrategy"] = "UseIPv4"
	data, err := json.Marshal(dns)
	if err != nil {
		logger.Warning("egress profile: DNS marshal failed:", err)
		return
	}
	config.DNSConfig = data
}

func applyEgressProfileRouting(config *xray.Config, profile EgressProfile, inboundTags []string) {
	if len(config.RouterConfig) == 0 {
		return
	}
	var routing map[string]any
	if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
		return
	}
	rulesRaw, ok := routing["rules"].([]any)
	if !ok {
		rulesRaw = []any{}
	}

	var inject []any
	if profile.IranDirect {
		inject = append(inject,
			map[string]any{
				"type":        "field",
				"outboundTag": "direct",
				"ip":          []any{"ext:geoip_IR.dat:ir"},
			},
			map[string]any{
				"type":        "field",
				"outboundTag": "direct",
				"domain":      []any{"ext:geosite_IR.dat:ir", "regexp:.*\\.ir$"},
			},
		)
	}

	tags := filterEgressInboundTags(inboundTags)
	if len(tags) > 0 {
		tagList := make([]any, len(tags))
		for i, t := range tags {
			tagList[i] = t
		}
		inject = append(inject, map[string]any{
			"type":        "field",
			"inboundTag":  tagList,
			"outboundTag": profile.OutboundTag,
		})
	} else {
		inject = append(inject, map[string]any{
			"type":        "field",
			"network":     "tcp,udp",
			"outboundTag": profile.OutboundTag,
		})
	}

	insertAt := egressProfileInsertIndex(rulesRaw)
	rulesRaw = append(rulesRaw[:insertAt], append(inject, rulesRaw[insertAt:]...)...)

	routing["rules"] = rulesRaw
	data, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("egress profile: routing marshal failed:", err)
		return
	}
	config.RouterConfig = data
}

// egressProfileInsertIndex places injected rules after the template's early
// system rules (api, blocked) but before VPN backstop rules appended by
// translateVpnRoutingRules.
func egressProfileInsertIndex(rules []any) int {
	if n := len(rules); n >= 2 {
		if isVpnBackstopRule(rules[n-1]) {
			if n >= 2 && isVpnBackstopRule(rules[n-2]) {
				return n - 2
			}
			return n - 1
		}
	}
	// After api + blocked rules in the default template.
	for i, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if inboundTag, ok := rule["inboundTag"].([]any); ok {
			for _, t := range inboundTag {
				if ts, ok := t.(string); ok && ts == "api" {
					return i + 1
				}
			}
		}
	}
	if len(rules) > 3 {
		return 3
	}
	return len(rules)
}

func isVpnBackstopRule(raw any) bool {
	rule, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	src, ok := rule["source"].([]any)
	if !ok || len(src) == 0 {
		return false
	}
	for _, s := range src {
		str, ok := s.(string)
		if !ok {
			continue
		}
		if str == vpnAddrSpace || strings.HasPrefix(str, "10.") {
			return true
		}
	}
	return false
}

func filterEgressInboundTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || t == "api" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func parseDNSServerList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n'
	}) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// OutboundTagExists reports whether tag appears among configured outbounds.
func OutboundTagExists(config *xray.Config, tag string) bool {
	if config == nil || tag == "" || len(config.OutboundConfigs) == 0 {
		return false
	}
	var obs []map[string]any
	if err := json.Unmarshal(config.OutboundConfigs, &obs); err != nil {
		return false
	}
	for _, ob := range obs {
		if t, _ := ob["tag"].(string); t == tag {
			return true
		}
	}
	return false
}
