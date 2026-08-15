package netsafe

import (
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"127.0.0.1", true},
		{"10.0.0.5", true},
		{"172.16.1.1", true},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"::1", true},
		{"2001:4860:4860::8888", false},
		{"fc00::1", true},
	}
	for _, tc := range cases {
		got := IsBlockedIP(net.ParseIP(tc.ip))
		if got != tc.blocked {
			t.Errorf("%s: blocked=%v want %v", tc.ip, got, tc.blocked)
		}
	}
}
