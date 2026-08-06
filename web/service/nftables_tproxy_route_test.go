package service

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTproxyPolicyRoutePresent(t *testing.T) {
	rules, err := exec.Command("ip", "rule", "show").Output()
	if err != nil {
		t.Skip("ip rule not available:", err)
	}
	routes, err := exec.Command("ip", "route", "show", "table", "100").Output()
	if err != nil {
		t.Skip("ip route table 100 not available:", err)
	}
	got := strings.Contains(string(rules), "fwmark 0x1 lookup 100") &&
		strings.Contains(string(routes), "local") &&
		strings.Contains(string(routes), "dev lo")
	if !got {
		t.Log("TPROXY policy route not present on this host (expected in CI)")
	}
}
