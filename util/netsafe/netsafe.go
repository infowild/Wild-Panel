package netsafe

import (
	"context"
	"fmt"
	"net"
	"time"
)

type allowPrivateKey struct{}

// ContextWithAllowPrivate marks a dial context so private/loopback targets are permitted.
func ContextWithAllowPrivate(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, allowPrivateKey{}, allow)
}

func allowPrivate(ctx context.Context) bool {
	v, _ := ctx.Value(allowPrivateKey{}).(bool)
	return v
}

// IsBlockedIP reports whether ip is loopback, unspecified, link-local, or private.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 already covered by link-local
		if ip4[0] == 10 {
			return true
		}
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		if ip4[0] == 127 {
			return true
		}
		return false
	}
	// IPv6 unique local fc00::/7 and site-local deprecated fec0::/10
	if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return true
	}
	return false
}

// DialContext resolves host and dials only non-blocked addresses unless the
// context allows private targets. Used by the node HTTP client to prevent SSRF.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	var last error
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	allow := allowPrivate(ctx)
	for _, ipa := range ips {
		if !allow && IsBlockedIP(ipa.IP) {
			last = fmt.Errorf("refusing private/loopback address %s", ipa.IP)
			continue
		}
		var target string
		if ipa.IP.To4() != nil {
			target = net.JoinHostPort(ipa.IP.String(), port)
		} else {
			target = net.JoinHostPort("["+ipa.IP.String()+"]", port)
		}
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no dialable address for %s", host)
	}
	return nil, last
}
