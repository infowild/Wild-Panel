package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/backend"
	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// MenuScriptPath is where the Wild Panel management menu is installed by
// `wild-panel-amd64 install-menu` (which deploy.sh runs on every install and update).
// Declared here, in the package that must REMOVE it, so the installer in main.go
// and this teardown can never drift apart on the path.
const MenuScriptPath = "/usr/bin/wild-panel"

// LegacyMenuScriptPath is the pre-rebrand menu path. Kept so uninstall and
// install-menu can clean up / refresh the old command without leaving a stale
// script that points at the wrong binary.
const LegacyMenuScriptPath = "/usr/bin/vpn-ui"

// UninstallOptions configures a host teardown.
type UninstallOptions struct {
	// ExePath is the running panel binary, used to kill any *other* panel
	// instance and to resolve a relative bin/ dir against the binary's directory.
	ExePath string
}

// UninstallReport records the outcome of a best-effort teardown: what was
// removed, what was deliberately kept (and must be removed by hand), and any
// errors encountered along the way (teardown never aborts on a single failure).
type UninstallReport struct {
	Removed []string
	Kept    []string
	Errors  []string
}

func (r *UninstallReport) fail(what string, err error) {
	r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", what, err))
}

// Uninstall reverses everything the panel installs on the host. It is the
// inverse of provisioning + `--systemd`, ordered processes/services → firewall
// → routing → files so nothing is in use when its backing files are removed. The
// database and the binary itself are left to the caller (main.runUninstall).
//
// Distro packages (libreswan, nftables, iproute2, kernel modules) and the
// irreversible boot-default / modprobe-blacklist edits are intentionally kept
// and reported for the operator. Must run as root.
func Uninstall(opts UninstallOptions) *UninstallReport {
	r := &UninstallReport{}
	logger.Info("uninstall: starting host teardown")

	// 1. The panel's own systemd unit (default "wild-panel"). Also always try the
	//    pre-rebrand "vpn-ui" unit so a migrated host does not leave a second unit
	//    enabled after the operator uninstalls from the new name.
	var sd SystemdService
	name := sd.GetServiceName()
	removedUnits := map[string]bool{}
	for _, u := range []string{name, "wild-panel", "vpn-ui"} {
		if u == "" || removedUnits[u] {
			continue
		}
		removedUnits[u] = true
		up := unitPath(u)
		_, statErr := os.Lstat(up)
		if err := sd.RemoveService(u); err != nil {
			r.fail("remove systemd unit "+u, err)
			continue
		}
		if statErr == nil {
			r.Removed = append(r.Removed, up)
		}
	}

	// 1b. Management menu + legacy symlink. Unlinking while this is the script
	//     running uninstall is safe on Linux (bash holds an open fd on the inode).
	removePath(r, MenuScriptPath)
	removePath(r, LegacyMenuScriptPath)

	// The panel's control socket, now that step 1 stopped the panel that served it.
	removePath(r, ControlSocketPath())
	// Legacy socket name next to older binaries.
	if opts.ExePath != "" {
		removePath(r, filepath.Join(filepath.Dir(opts.ExePath), "vpn-ui.sock"))
		removePath(r, filepath.Join(filepath.Dir(opts.ExePath), "wild-panel.sock"))
	}

	// 2. Stop/kill the daemons a live panel supervised (our fresh process's
	//    procMgr is empty, so fall back to pkill by resolved binary path).
	stopVpnDaemons(r, opts.ExePath)

	// 2b. Client-side VPN outbound tunnels.
	var vpnOut VpnOutboundService
	if tunnels := vpnOut.List(); len(tunnels) > 0 {
		vpnOut.StopAll()
		for _, t := range tunnels {
			r.Removed = append(r.Removed, "vpn outbound tunnel "+t.Tag+" ("+t.Kind+")")
		}
	}

	// 3. Host ipsec.service (only present on the non-bundled libreswan path).
	if commandExists("systemctl") {
		_, _ = systemctl("disable", "--now", "ipsec")
	}

	// 4. Cloudflare warp-cli (SOCKS5), via its own bundled uninstaller.
	uninstallWarpSocks(r)

	// 5. Legacy per-daemon systemd units (superseded by the child-process design;
	//    removed defensively in case an old install left them behind).
	for _, u := range []string{"xl2tpd", "openvpn-server@", "pptpd"} {
		p := unitPath(u)
		if _, err := os.Lstat(p); err == nil {
			if commandExists("systemctl") {
				_, _ = systemctl("disable", "--now", u)
			}
			removePath(r, p)
		}
	}
	if commandExists("systemctl") {
		_, _ = systemctl("daemon-reload")
	}

	// 6. nftables table + legacy iptables chains + firewalld trust.
	if commandExists("nft") {
		_ = exec.Command("nft", "delete", "table", "ip", "vpn").Run()
	}
	(&NftService{}).CleanupLegacyIptables()
	if firewalldRunning() {
		_ = exec.Command("firewall-cmd", "--zone=trusted", "--remove-source="+vpnAddrSpace).Run()
		_ = exec.Command("firewall-cmd", "--permanent", "--zone=trusted", "--remove-source="+vpnAddrSpace).Run()
	}

	// 7. Policy routing (fwmark 1 → table 100). Not reversed anywhere else.
	if commandExists("ip") {
		for i := 0; i < 10; i++ {
			if err := exec.Command("ip", "rule", "del", "fwmark", "1", "lookup", "100").Run(); err != nil {
				break
			}
		}
		_ = exec.Command("ip", "route", "flush", "table", "100").Run()
	}

	// 8. /etc configs, runtime dirs, seq files, logs.
	for _, p := range []string{
		"/etc/vpn-ui", // nft config dir (vpn.nft)
		"/etc/xl2tpd/xl2tpd.conf",
		"/etc/ppp/options.xl2tpd",
		"/etc/ipsec.conf",
		"/etc/ipsec.secrets",
		"/etc/pptpd.conf",
		"/etc/ppp/pptpd-options",
		"/etc/ppp/radius", // panel-owned subdir of the host /etc/ppp
		"/etc/swanctl/conf.d/l2tp.conf",
		"/etc/modules-load.d/vpn-ui.conf",
		"/etc/sysctl.d/99-vpn-ui.conf",
		// Outbound client config trees (not covered by inbound coreCatalog paths).
		"/etc/vpn-ui-l2tp-out",
		"/etc/vpn-ui-sstp-out",
		"/etc/vpn-ui-ikev2",
	} {
		removePath(r, p)
	}
	// Per-inbound OpenVPN + OpenConnect outbound dirs.
	if matches, _ := filepath.Glob("/etc/openvpn/server-*"); len(matches) > 0 {
		for _, m := range matches {
			removePath(r, m)
		}
	}
	if matches, _ := filepath.Glob("/etc/openconnect/out-*"); len(matches) > 0 {
		for _, m := range matches {
			removePath(r, m)
		}
	}
	if matches, _ := filepath.Glob("/etc/ppp/options.pptp-out-*"); len(matches) > 0 {
		for _, m := range matches {
			removePath(r, m)
		}
	}
	for _, p := range []string{"/var/run/xl2tpd", "/var/run/openvpn", "/run/pluto"} {
		removePath(r, p)
	}
	if matches, _ := filepath.Glob("/var/run/radius-*.seq"); len(matches) > 0 {
		for _, m := range matches {
			removePath(r, m)
		}
	}
	removePath(r, config.GetLogFolder()) // /var/log/wild-panel
	removePath(r, "/var/log/vpn-ui")     // pre-rebrand log dir
	removePath(r, "/var/log/pluto.log")

	// 9. Bundled daemon trees + their host symlinks.
	removeSymlinkIfTarget(r, backend.PppdSystem, backend.PppdBundled)
	removeSymlinkIfTarget(r, backend.PppdPluginDir, backend.PppdBundleRoot+"/lib/pppd")
	removePath(r, backend.PppdBundleRoot) // /usr/libexec/vpn-ui (incl. libreswan/, pptpctrl)
	removePath(r, backend.AccelBundleRoot)
	removePath(r, backend.StrongswanBundleRoot)
	removePath(r, backend.SstpcBundleRoot)
	if usingBundledIpsec() {
		removePath(r, backend.LibreswanNssDir) // /etc/ipsec.d — only ours on the bundled path
	}

	// 9b. Everything the CORE CATALOG owns, driven off the catalog rather than a
	//     hand-written list.
	for _, spec := range coreCatalog {
		if spec.builtin {
			continue
		}
		for _, p := range spec.paths {
			removePath(r, p)
		}
		for _, g := range spec.globs {
			matches, err := filepath.Glob(g)
			if err != nil {
				continue
			}
			for _, m := range matches {
				removePath(r, m)
			}
		}
	}
	seenFeat := map[string]bool{}
	for _, spec := range coreCatalog {
		for _, f := range spec.feats {
			if seenFeat[f] || f == featPppd || f == featPptpCtrl || f == featKernelMods {
				continue
			}
			seenFeat[f] = true
			if step := removeFeature(f); step.Msg != "" {
				r.Removed = append(r.Removed, step.Name+": "+step.Msg)
			}
		}
	}

	// 10. The bin/ dir next to the binary (xray core, geo files, config.json).
	binDir := config.GetBinFolderPath()
	base := "."
	if opts.ExePath != "" {
		base = filepath.Dir(opts.ExePath)
	}
	if !filepath.IsAbs(binDir) {
		binDir = filepath.Join(base, binDir)
	}
	removePath(r, binDir)

	// 10b. cert/ + backups/ beside the binary.
	removePath(r, filepath.Join(base, "cert"))
	removePath(r, filepath.Join(base, "backups"))

	// 10c. Scrub the other install root after a rebrand migrate (/opt/vpn-ui ↔
	//      /opt/wild-panel) so uninstall from one name does not leave the sibling
	//      tree with DB/binary leftovers.
	for _, dir := range []string{"/opt/wild-panel", "/opt/vpn-ui"} {
		if opts.ExePath != "" && filepath.Clean(dir) == filepath.Clean(base) {
			continue // caller removes this dir after deleting the binary
		}
		scrubInstallDir(r, dir)
	}

	// 11. Kept — not removed (shared, or irreversible without a backup we never took).
	r.Kept = append(r.Kept,
		"distro packages (libreswan, nftables, iproute2/iproute, kernel-modules-extra) — remove with your package manager if unused elsewhere",
		"GRUB boot-default pin (GRUB_DEFAULT=saved in /etc/default/grub) — not reversible without your original",
		"/etc/modprobe.d un-blacklist edits — not reversible without your original",
	)

	logger.Info("uninstall: host teardown complete")
	return r
}

// scrubInstallDir removes known panel leftovers under an install root (binary,
// databases, cert, backups, bin/, sockets) and then rmdirs the root if empty.
func scrubInstallDir(r *UninstallReport, dir string) {
	if dir == "" || dir == "/" || dir == "." {
		return
	}
	if _, err := os.Lstat(dir); err != nil {
		return
	}
	for _, name := range []string{
		"wild-panel-amd64", "vpn-ui-amd64", "wild-panel", "vpn-ui",
		"wild-panel.db", "wild-panel.db-wal", "wild-panel.db-shm", "wild-panel.db-journal",
		"vpn-ui.db", "vpn-ui.db-wal", "vpn-ui.db-shm", "vpn-ui.db-journal",
		"x-ui.db", "x-ui.db-wal", "x-ui.db-shm", "x-ui.db-journal",
		"wild-panel.sock", "vpn-ui.sock",
		"cert", "backups", "bin",
	} {
		removePath(r, filepath.Join(dir, name))
	}
	if err := os.Remove(dir); err == nil {
		r.Removed = append(r.Removed, dir)
	}
}

// stopVpnDaemons stops the supervised VPN daemons. procMgr.StopAll covers
// daemons this process started (a no-op for a fresh --uninstall invocation);
// pkill by resolved binary path then catches daemons a separately-running panel
// spawned, mirroring procmgr.go's orphan cleanup.
func stopVpnDaemons(r *UninstallReport, exePath string) {
	procMgr.StopAll()
	if !commandExists("pkill") {
		return
	}
	// accel-pppd and telemt were missing here even though the orphan reaper in
	// procmgr.go has known about them for as long as they have existed. Same
	// omission, same consequence: a daemon still holding :443 (or the MTProto
	// port) after the panel that supervised it is gone.
	for _, d := range []string{"openvpn", "xl2tpd", "pptpd", "accel-pppd", "telemt"} {
		bin := daemonBin(d)
		if bin == d {
			continue // unresolved bare name — avoid a too-broad match
		}
		_ = exec.Command("pkill", "-KILL", "-f", bin).Run()
	}
	// Both IPsec planes. libreswan's pluto is the legacy one; charon is the
	// SHARED plane that L2TP and IKEv2 both run on, so on any host that had
	// either of them a surviving charon holding UDP 500/4500 is the NORMAL
	// outcome, not an edge case.
	for _, p := range []string{
		backend.LibreswanBundleRoot + "/libexec/ipsec/pluto.bin",
		backend.StrongswanBundleRoot + "/libexec/ipsec/charon.bin",
	} {
		_ = exec.Command("pkill", "-KILL", "-f", p).Run()
	}
	// ocserv RETITLES its processes, so a -f match on the binary path misses
	// them; only an exact-name pass finds it. Same list procmgr.go reaps.
	for _, n := range []string{"ocserv-main", "ocserv-sm", "ocserv-worker", "ocserv"} {
		_ = exec.Command("pkill", "-KILL", "-x", n).Run()
	}
	// The Xray core is not a procMgr child, so nothing else stops it: the panel
	// is SIGKILLed below, which skips any shutdown hook, and it would be left
	// holding every inbound port plus the API port.
	if bin := xray.GetBinaryPath(); bin != "" {
		_ = exec.Command("pkill", "-KILL", "-f", bin).Run()
	}

	// Kill any OTHER panel process (e.g. the one the just-removed unit ran).
	// Exclude ourselves AND our ancestor chain: `pgrep -f <exePath>` also matches
	// the wrapper that launched us (under `incus exec`/ssh, `sh -c "<exePath>
	// --uninstall ..."` carries the exe path), and killing that parent severs the
	// caller's exec channel -> spurious 255 exit though teardown still completes.
	if exePath == "" {
		return
	}
	skip := map[string]bool{}
	for pid := os.Getpid(); pid > 1; {
		skip[strconv.Itoa(pid)] = true
		ppid := parentPID(pid)
		if ppid <= 1 || ppid == pid {
			break
		}
		pid = ppid
	}
	killOthers := func(pids []string) {
		for _, pid := range pids {
			if skip[pid] {
				continue
			}
			_ = exec.Command("kill", "-KILL", pid).Run()
		}
	}
	// Reap panel binaries by well-known basenames. Do NOT match `wild-panel` /
	// `vpn-ui` — those are the management menu scripts; killing them mid-uninstall
	// would abort the teardown that the menu just started.
	// pkill -x <name> was wrong: it has no PID exclusion, so it SIGKILL'd the
	// uninstall process itself (vpn-ui-amd64 is 12 chars and matches comm
	// exactly). Linux comm is also truncated to 15 chars, so try both.
	for _, n := range []string{"wild-panel-amd64", "vpn-ui-amd64"} {
		out, _ := exec.Command("pgrep", "-x", n).Output()
		killOthers(strings.Fields(string(out)))
		if len(n) > 15 {
			out, _ = exec.Command("pgrep", "-x", n[:15]).Output()
			killOthers(strings.Fields(string(out)))
		}
	}
	out, _ := exec.Command("pgrep", "-f", exePath).Output()
	killOthers(strings.Fields(string(out)))
}

// parentPID returns the parent PID of pid by reading /proc/<pid>/stat, or 0 if it
// can't be determined. The comm field (2nd) may contain spaces and parentheses, so
// parse the fields AFTER the last ')': ppid is the second of those.
func parentPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+1 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

// uninstallWarpSocks removes the official Cloudflare warp-cli (if present) via
// its bundled installer's --uninstall path, blocking until the background run
// finishes — a returning CLI would otherwise kill the goroutine mid-uninstall.
func uninstallWarpSocks(r *UninstallReport) {
	if !WarpSocksInstalled() {
		return
	}
	logger.Info("uninstall: removing cloudflare warp-cli")
	if !StartWarpSocks("uninstall", 0) {
		r.fail("warp uninstall", fmt.Errorf("another warp-cli run is already in progress"))
		return
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		if st := WarpSocksState(); st.Done {
			if st.Success {
				r.Removed = append(r.Removed, "cloudflare-warp (warp-cli SOCKS)")
			} else {
				r.fail("warp uninstall", fmt.Errorf("uninstaller reported failure"))
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	r.fail("warp uninstall", fmt.Errorf("timed out after 3m"))
}

// removePath deletes a file or directory tree, recording the outcome. A path
// that's already absent is silently skipped (not an error).
func removePath(r *UninstallReport, path string) {
	if path == "" {
		return
	}
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			r.fail("stat "+path, err)
		}
		return
	}
	if err := os.RemoveAll(path); err != nil {
		r.fail("remove "+path, err)
		return
	}
	r.Removed = append(r.Removed, path)
}

// removeSymlinkIfTarget removes link only when it is a symlink pointing at
// wantTarget — so we never unlink a distro's own file that happens to share the
// path (e.g. a host-native /usr/sbin/pppd).
func removeSymlinkIfTarget(r *UninstallReport, link, wantTarget string) {
	dest, err := os.Readlink(link)
	if err != nil || dest != wantTarget {
		return
	}
	if err := os.Remove(link); err != nil {
		r.fail("remove symlink "+link, err)
		return
	}
	r.Removed = append(r.Removed, link+" -> "+wantTarget)
}
