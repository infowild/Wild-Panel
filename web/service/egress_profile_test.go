package service

import (
	"encoding/json"
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
