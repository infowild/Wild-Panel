package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/xray"
)

func TestApplyEgressProfileRouting(t *testing.T) {
	routing := map[string]any{
		"domainStrategy": "AsIs",
		"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	}
	raw, _ := json.Marshal(routing)
	cfg := &xray.Config{RouterConfig: raw}

	ApplyEgressProfile(cfg, EgressProfile{
		Enabled:     true,
		OutboundTag: "intl-exit",
		IranDirect:  true,
	}, []string{"api", "l2tp-1", "vless-1"})

	var out map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &out); err != nil {
		t.Fatal(err)
	}
	rules, ok := out["rules"].([]any)
	if !ok || len(rules) < 4 {
		t.Fatalf("expected injected rules, got %d", len(rules))
	}
	foundIntl := false
	foundIran := false
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if ob, _ := rule["outboundTag"].(string); ob == "intl-exit" {
			foundIntl = true
			tags, _ := rule["inboundTag"].([]any)
			if len(tags) != 2 {
				t.Fatalf("inboundTag count = %d want 2 (api excluded)", len(tags))
			}
		}
		if ob, _ := rule["outboundTag"].(string); ob == "direct" {
			if ips, _ := rule["ip"].([]any); len(ips) > 0 {
				foundIran = true
			}
		}
	}
	if !foundIntl || !foundIran {
		t.Fatalf("intl=%v iran=%v", foundIntl, foundIran)
	}
	// Iran rules must use panel geo files, not tags that may be absent from geosite.dat.
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if domains, _ := rule["domain"].([]any); len(domains) > 0 {
			for _, d := range domains {
				if ds, _ := d.(string); strings.HasPrefix(ds, "geosite:category-ir") {
					t.Fatalf("unexpected geosite tag %q", ds)
				}
			}
		}
	}
}

func TestEgressProfileInsertBeforeBackstop(t *testing.T) {
	rules := []any{
		map[string]any{"type": "field", "outboundTag": "direct", "source": []any{"10.0.1.2"}},
		map[string]any{"type": "field", "outboundTag": "blocked", "source": []any{vpnAddrSpace}},
	}
	idx := egressProfileInsertIndex(rules)
	if idx != 0 {
		t.Fatalf("insert index = %d want 0", idx)
	}
}

// The blackout deadlock: public DNS has to leave through the tunnel, while the
// tunnel's own hostname has to resolve without it.
func TestEgressProfileDnsEscapesTheBlackout(t *testing.T) {
	routing := map[string]any{
		"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "outboundTag": "blocked", "ip": []any{"geoip:private"}},
		},
	}
	raw, _ := json.Marshal(routing)
	cfg := &xray.Config{
		RouterConfig: raw,
		OutboundConfigs: []byte(`[
			{"tag":"direct","protocol":"freedom"},
			{"tag":"intl","protocol":"vless","settings":{"vnext":[{"address":"movie.example.ir","port":443}]}}
		]`),
	}

	ApplyEgressProfile(cfg, EgressProfile{
		Enabled:     true,
		OutboundTag: "intl",
		DnsEnabled:  true,
		DnsServers:  []string{"1.1.1.1", "8.8.8.8"},
	}, []string{"inbound-443"})

	var out map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &out); err != nil {
		t.Fatal(err)
	}
	rules, _ := out["rules"].([]any)

	dnsIdx, guardIdx := -1, -1
	for i, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if rule["port"] == "53" && rule["outboundTag"] == "intl" {
			dnsIdx = i
		}
		if doms, _ := rule["domain"].([]any); len(doms) == 1 && doms[0] == "full:movie.example.ir" {
			if rule["outboundTag"] == "direct" {
				guardIdx = i
			}
		}
	}
	if dnsIdx < 0 {
		t.Fatal("no rule pinning public DNS to the egress outbound")
	}
	if guardIdx < 0 {
		t.Fatal("no direct rule for the egress outbound's own hostname")
	}
	if guardIdx > dnsIdx {
		t.Fatalf("hostname guard (%d) must precede the DNS rule (%d)", guardIdx, dnsIdx)
	}

	var dns map[string]any
	if err := json.Unmarshal(cfg.DNSConfig, &dns); err != nil {
		t.Fatal(err)
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 3 {
		t.Fatalf("dns servers = %v, want bootstrap + 2 public", servers)
	}
	first, ok := servers[0].(map[string]any)
	if !ok || first["address"] != "localhost" {
		t.Fatalf("first dns server must resolve the tunnel hostname locally, got %v", servers[0])
	}
}

func TestEgressOutboundHosts(t *testing.T) {
	outbounds := []byte(`[
		{"tag":"intl","protocol":"vless","settings":{"vnext":[{"address":"a.example.com"},{"address":"203.0.113.9"}]}},
		{"tag":"wg","protocol":"wireguard","settings":{"peers":[{"endpoint":"engage.example.com:2408"}]}}
	]`)

	domains, ips := egressOutboundHosts(outbounds, "intl")
	if len(domains) != 1 || domains[0] != "a.example.com" {
		t.Fatalf("domains = %v", domains)
	}
	if len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Fatalf("ips = %v", ips)
	}

	domains, _ = egressOutboundHosts(outbounds, "wg")
	if len(domains) != 1 || domains[0] != "engage.example.com" {
		t.Fatalf("wireguard endpoint port not stripped: %v", domains)
	}

	if d, i := egressOutboundHosts(outbounds, "missing"); len(d) != 0 || len(i) != 0 {
		t.Fatalf("unknown tag returned %v / %v", d, i)
	}
}

func TestDnsServerIPsSkipsNonAddresses(t *testing.T) {
	got := dnsServerIPs([]string{"1.1.1.1", "https://dns.example/dns-query", "localhost", "8.8.8.8"})
	if len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "8.8.8.8" {
		t.Fatalf("got %v", got)
	}
}

func TestParseDNSServerList(t *testing.T) {
	got := parseDNSServerList("1.1.1.1, 8.8.8.8;9.9.9.9")
	if len(got) != 3 || got[0] != "1.1.1.1" || got[2] != "9.9.9.9" {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestOutboundTagExists(t *testing.T) {
	cfg := &xray.Config{
		OutboundConfigs: []byte(`[{"tag":"direct"},{"tag":"intl-exit","protocol":"freedom"}]`),
	}
	if !OutboundTagExists(cfg, "intl-exit") || OutboundTagExists(cfg, "missing") {
		t.Fatal("tag existence mismatch")
	}
}

func TestVpnBackstopTagsUsesEgressWhenPresent(t *testing.T) {
	outbounds := []byte(`[
		{"tag":"direct","protocol":"freedom"},
		{"tag":"blocked","protocol":"blackhole"},
		{"tag":"InfoWild-Emergency-Abolfazl","protocol":"vless"}
	]`)
	def, block := vpnBackstopTags(outbounds, "InfoWild-Emergency-Abolfazl")
	if def != "InfoWild-Emergency-Abolfazl" {
		t.Fatalf("defaultTag = %q want egress outbound", def)
	}
	if block != "blocked" {
		t.Fatalf("blockTag = %q want blocked", block)
	}
}

func TestVpnBackstopTagsFallsBackToFirstOutbound(t *testing.T) {
	outbounds := []byte(`[
		{"tag":"direct","protocol":"freedom"},
		{"tag":"blocked","protocol":"blackhole"}
	]`)
	def, block := vpnBackstopTags(outbounds, "")
	if def != "direct" || block != "blocked" {
		t.Fatalf("got default=%q block=%q", def, block)
	}
}

func TestVpnBackstopTagsEgressWinsEvenIfMissingFromList(t *testing.T) {
	// Profile tag must win even when outbounds JSON is empty/unreadable — mirrors
	// ApplyEgressProfile, which injects the tag without requiring a list match.
	def, _ := vpnBackstopTags(nil, "InfoWild-Emergency-Abolfazl")
	if def != "InfoWild-Emergency-Abolfazl" {
		t.Fatalf("got %q want egress tag", def)
	}
	def, _ = vpnBackstopTags([]byte(`not-json`), "intl-exit")
	if def != "intl-exit" {
		t.Fatalf("got %q want intl-exit", def)
	}
}
