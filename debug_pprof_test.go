package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"

	"github.com/op/go-logging"
)

func init() { logger.InitLogger(logging.CRITICAL) }

// The profiler hands out goroutine stacks, heap contents and the process command line.
// Binding it anywhere but loopback publishes all of that, so every non-loopback form has
// to be refused rather than "helpfully" accepted.
func TestPprofRefusesNonLoopbackAddresses(t *testing.T) {
	refused := []string{
		":6060",          // every interface — the dangerous shorthand
		"0.0.0.0:6060",   // explicit all-interfaces
		"[::]:6060",      // all-interfaces, v6
		"1.2.3.4:6060",   // some public address
		"example.com:80", // a name that is not an IP at all
	}
	for _, addr := range refused {
		// Use a port nothing else on the machine will hold, so a successful dial can
		// only mean this call bound it.
		port := freePort(t)
		probe := addr
		if i := strings.LastIndex(addr, ":"); i >= 0 {
			probe = addr[:i] + ":" + port
		}
		t.Setenv("WILDPANEL_PPROF", probe)
		startPprofIfRequested()
		time.Sleep(100 * time.Millisecond)

		// If the profiler wrongly bound ":port" or "0.0.0.0:port", loopback reaches it.
		if listening("127.0.0.1:" + port) {
			t.Fatalf("%q was accepted and is listening; the profiler must refuse anything not on loopback", probe)
		}
	}
}

// Unset means the feature does not exist: no listener, no port, nothing to find.
func TestPprofIsOffByDefault(t *testing.T) {
	os.Unsetenv("WILDPANEL_PPROF")
	os.Unsetenv("VPNUI_PPROF")
	port := freePort(t)
	startPprofIfRequested()
	time.Sleep(100 * time.Millisecond)
	if listening("127.0.0.1:" + port) {
		t.Fatal("a listener appeared with no env var set; the profiler must be opt-in")
	}
}

// A bare port is the shorthand operators actually type. It must resolve to loopback,
// never to every interface.
func TestBarePortBindsLoopbackOnly(t *testing.T) {
	port := freePort(t)
	t.Setenv("WILDPANEL_PPROF", port)
	startPprofIfRequested()

	if !waitForListen("127.0.0.1:"+port, 2*time.Second) {
		t.Fatal("profiler did not come up on loopback")
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/debug/pprof/")
	if err != nil {
		t.Fatalf("index not served: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index returned %d", resp.StatusCode)
	}
}

// listening reports whether anything currently accepts connections on addr.
func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return port
}

func waitForListen(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
