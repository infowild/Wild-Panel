package service

import "testing"

func TestTproxyPolicyRouteInRuleShow(t *testing.T) {
	cases := []struct {
		line  string
		match bool
	}{
		{"32765:\tfrom all fwmark 0x1 lookup 100\n", true},
		{"32764:\tfrom all fwmark 0x1/0xffffffff lookup 100\n", true},
		{"32766:\tfrom all lookup main\n", false},
	}
	for _, c := range cases {
		if got := tproxyPolicyRouteInRuleShow(c.line); got != c.match {
			t.Fatalf("line %q: got %v want %v", c.line, got, c.match)
		}
	}
}

func TestUfwStatusAllowsSource(t *testing.T) {
	status := `
To                         Action      From
--                         ------      ----
2053/tcp                   ALLOW       Anywhere
Anywhere                   ALLOW       10.0.0.0/12
`
	if !ufwStatusAllowsSource(status, "10.0.0.0/12") {
		t.Fatal("expected VPN CIDR to be detected")
	}
	if ufwStatusAllowsSource(status, "192.168.0.0/16") {
		t.Fatal("unexpected match")
	}
}
