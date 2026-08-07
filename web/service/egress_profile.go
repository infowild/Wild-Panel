package service

import (
	"encoding/json"
	"net"
	"sort"
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
	// The egress outbound's own endpoint has to stay reachable WITHOUT the tunnel it
	// is supposed to build, so both the DNS override and the routing injection below
	// are given its hostnames to carve out.
	egressDomains, egressIPs := egressOutboundHosts(config.OutboundConfigs, tag)

	if profile.DnsEnabled {
		applyEgressProfileDNS(config, profile.DnsServers, egressDomains)
	}
	applyEgressProfileRouting(config, profile, inboundTags, egressDomains, egressIPs)
}

// applyEgressProfileDNS points Xray at the public resolvers, but resolves the egress
// outbound's OWN hostname with the host resolver first.
//
// Without that carve-out the profile deadlocks during a blackout: every name (including
// the outbound's server address) is queried against 1.1.1.1/8.8.8.8, those queries are
// generated internally so they carry no inboundTag, and the tunnel that would carry them
// cannot come up until its own hostname resolves. The panel then looks healthy — Xray
// running, TPROXY correct, inbounds bound — while every protocol has no internet.
func applyEgressProfileDNS(config *xray.Config, servers []string, egressDomains []string) {
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
	serverList := make([]any, 0, len(servers)+1)
	if len(egressDomains) > 0 {
		domains := make([]any, 0, len(egressDomains))
		for _, d := range egressDomains {
			domains = append(domains, "full:"+d)
		}
		serverList = append(serverList, map[string]any{
			"address": "localhost",
			"domains": domains,
		})
	}
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

// egressOutboundHosts returns the hostnames and literal IPs the outbound tagged tag
// dials, split so callers can build domain rules and ip rules separately. Walks the
// outbound's JSON for the address-carrying keys every protocol shape uses (vnext /
// servers "address", wireguard peer "endpoint") rather than special-casing protocols.
func egressOutboundHosts(outboundConfigs []byte, tag string) (domains, ips []string) {
	if len(outboundConfigs) == 0 || tag == "" {
		return nil, nil
	}
	var obs []map[string]any
	if err := json.Unmarshal(outboundConfigs, &obs); err != nil {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, ob := range obs {
		if t, _ := ob["tag"].(string); t != tag {
			continue
		}
		for _, host := range collectHostValues(ob["settings"]) {
			if host == "" || seen[host] {
				continue
			}
			seen[host] = true
			if net.ParseIP(host) != nil {
				ips = append(ips, host)
			} else {
				domains = append(domains, host)
			}
		}
	}
	// Map iteration order is random, and an unsorted list would make the generated
	// config differ byte-for-byte on every build — which RestartXray reads as a real
	// change and acts on, dropping every live connection each time.
	sort.Strings(domains)
	sort.Strings(ips)
	return domains, ips
}

func collectHostValues(node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		for key, val := range v {
			if s, ok := val.(string); ok && (key == "address" || key == "endpoint") {
				out = append(out, stripHostPort(s))
				continue
			}
			out = append(out, collectHostValues(val)...)
		}
	case []any:
		for _, item := range v {
			out = append(out, collectHostValues(item)...)
		}
	}
	return out
}

// stripHostPort trims a trailing ":port" from a wireguard-style endpoint while leaving
// bare IPv6 literals (which contain colons of their own) intact.
func stripHostPort(s string) string {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return strings.Trim(s, "[]")
}

func applyEgressProfileRouting(config *xray.Config, profile EgressProfile, inboundTags, egressDomains, egressIPs []string) {
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

	// Reaching the egress outbound must never depend on the egress outbound. These go
	// first so the bootstrap DNS lookup for its hostname — and any client that happens
	// to address the server directly — leave via "direct" instead of the tunnel.
	if len(egressDomains) > 0 {
		list := make([]any, len(egressDomains))
		for i, d := range egressDomains {
			list[i] = "full:" + d
		}
		inject = append(inject, map[string]any{
			"type": "field", "outboundTag": "direct", "domain": list,
		})
	}
	if len(egressIPs) > 0 {
		list := make([]any, len(egressIPs))
		for i, ip := range egressIPs {
			list[i] = ip
		}
		inject = append(inject, map[string]any{
			"type": "field", "outboundTag": "direct", "ip": list,
		})
	}

	// Public DNS must ride the tunnel. Xray generates these queries itself, so they
	// carry NO inboundTag and the per-inbound rule below cannot match them; left alone
	// they fall through to the template's first outbound ("direct") and, during a
	// blackout, simply time out — which reads as "connects, no internet" on every
	// protocol at once.
	if profile.DnsEnabled {
		if dnsIPs := dnsServerIPs(profile.DnsServers); len(dnsIPs) > 0 {
			inject = append(inject, map[string]any{
				"type":        "field",
				"outboundTag": profile.OutboundTag,
				"port":        "53",
				"ip":          dnsIPs,
			})
		}
	}

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
// translateVpnRoutingRules. The backstop itself also uses the egress outbound
// as its defaultTag when the profile is enabled (see vpnBackstopTags).
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

// dnsServerIPs keeps only the plain IP entries of a resolver list: a routing rule
// matches DNS traffic by destination IP, so DoH/DoT URLs and "localhost" have no
// address to pin and are skipped rather than emitted as an unmatchable rule.
func dnsServerIPs(servers []string) []any {
	var out []any
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s != "" && net.ParseIP(s) != nil {
			out = append(out, s)
		}
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
