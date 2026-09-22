package main

import (
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
)

// startPprofIfRequested exposes Go's profiler on a LOOPBACK address when the operator
// asks for it, and does nothing otherwise.
//
// It exists because "the panel is using too much CPU" cannot be answered by reading the
// code. This panel runs ~20 scheduled jobs at intervals from one second upwards, several
// of which parse every inbound's client JSON; which of them actually costs anything
// depends entirely on how many inbounds, clients and connected sessions a given install
// has. A CPU profile answers it in 30 seconds and names the exact function. Guessing has
// already cost two releases.
//
// OFF unless WILDPANEL_PPROF (or VPNUI_PPROF) is set, and REFUSED unless the address is
// a loopback one. The profiler's endpoints hand out goroutine stacks, heap contents and
// command-line arguments, so this must never be reachable from outside the host. An
// operator who needs it from elsewhere tunnels to it over SSH:
//
//	ssh -N -L 6060:127.0.0.1:6060 root@server
//	go tool pprof -top http://127.0.0.1:6060/debug/pprof/profile?seconds=30
//
// Deliberately NOT mounted on the panel's own router: that one is public, is reached
// through the session middleware, and a bug in how a profile route is gated would expose
// process memory. A separate listener on 127.0.0.1 cannot be reached at all from off-box,
// whatever the panel's auth does.
func startPprofIfRequested() {
	addr := strings.TrimSpace(os.Getenv("WILDPANEL_PPROF"))
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv("VPNUI_PPROF"))
	}
	if addr == "" {
		return
	}
	// Bare port ("6060") is the common shorthand and must not become ":6060", which
	// listens on every interface.
	if !strings.Contains(addr, ":") {
		addr = "127.0.0.1:" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		logger.Warning("pprof: bad address ", addr, ": ", err, " — profiler not started")
		return
	}
	// An empty host means "all interfaces". Refuse it rather than silently publishing
	// the process's memory to the internet.
	if host == "" {
		logger.Warning("pprof: refusing to listen on all interfaces; use 127.0.0.1:" + port)
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		logger.Warning("pprof: refusing non-loopback address ", host, "; use 127.0.0.1:"+port)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// No ReadTimeout/WriteTimeout: /profile?seconds=30 and /trace are long-lived by
		// design and a write deadline would truncate them into an unusable file.
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("pprof: profiler listening on http://" + addr + "/debug/pprof/ (loopback only)")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warning("pprof: listener stopped: ", err)
		}
	}()
}
