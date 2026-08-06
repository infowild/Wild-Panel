package service

import "testing"

func TestTproxyPolicyRouteInRuleShow(t *testing.T) {
	if !tproxyPolicyRouteInRuleShow("32765:\tfrom all fwmark 0x1 lookup 100\n") {
		t.Fatal("expected match")
	}
	if tproxyPolicyRouteInRuleShow("32766:\tfrom all lookup main\n") {
		t.Fatal("expected no match")
	}
}
