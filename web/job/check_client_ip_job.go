package job

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// IPWithTimestamp tracks an IP address with its last seen timestamp
type IPWithTimestamp struct {
	IP        string `json:"ip"`
	Timestamp int64  `json:"timestamp"`
}

// CheckClientIpJob records which source addresses Xray's access log attributed to each
// client, for DISPLAY ONLY: the panel's per-client IP log reads the rows it writes
// (POST /panel/api/inbounds/clientIps/:email, rendered by inbound_info_modal.html).
//
// It no longer ENFORCES the IP limit. That moved into the patched core, which refuses a
// connection at admission from the per-account cap the panel publishes in the
// speedlimits.json sidecar (web/service/speedlimit.go). What used to live here was a log
// scrape that wrote a [LIMIT_IP] line for a fail2ban jail to act on, and it was wrong in
// two ways no amount of tuning fixes: it banned by ADDRESS, so on carrier-grade NAT it
// took out unrelated paying customers who merely shared an egress, and it could not
// disconnect anyone anyway (RemoveUser -> validator.Del, but the VLESS validator is
// consulted exactly once per connection, at handshake, so live connections were untouched
// and the user was re-added 100ms later).
//
// So everything below is telemetry: it may be late, empty (the shipped access log default
// is "none"), or plain absent without any effect on the limit that is actually applied.
type CheckClientIpJob struct {
	lastClear int64

	// logOffset is how far into the access log the last scrape got, so the next one
	// reads only what Xray appended since.
	//
	// Without it this job re-parsed the ENTIRE access log every 10 seconds while the
	// log grew for a whole hour between truncations (see clearAccessLog). That is
	// quadratic in traffic: 360 passes an hour over an ever-larger file, each one
	// running three regexes per line. On a busy panel the log reaches hundreds of MB
	// within the hour and this single job saturates a core — and it scales with user
	// traffic, so it arrives as "the panel suddenly eats CPU" with nothing in the
	// config having changed.
	//
	// Reading only the tail is not an approximation: updateInboundClientIps MERGES
	// each scrape into the row already stored for that client (see mergeClientIps),
	// so lines parsed on an earlier pass are still represented. Re-reading them
	// produced the same merged result at 360x the cost.
	logOffset int64

	// limitIpCache / limitIpCachedAt memoize hasLimitIp, which otherwise JSON-decodes
	// every client on every inbound on every tick just to answer one boolean.
	limitIpCache    bool
	limitIpCachedAt int64
}

// Compiled once. These used to be built inside processLogFile, so every tick paid
// three regexp compilations before it read a single line.
var (
	accessIPRegex        = regexp.MustCompile(`from (?:tcp:|udp:)?\[?([0-9a-fA-F\.:]+)\]?:\d+ accepted`)
	accessEmailRegex     = regexp.MustCompile(`email: (.+)$`)
	accessTimestampRegex = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})`)
)

// limitIpCacheTTL bounds how stale the "does anyone have an IP cap" answer may be.
// This job is display-only telemetry, so noticing a freshly set cap up to a minute
// late costs nothing; re-deriving it every 10s cost a full scan of every client.
const limitIpCacheTTL = int64(60)

var job *CheckClientIpJob

// ipStaleAfterSeconds is how long an address stays listed for a client after the access
// log stops mentioning it.
//
// It is a display retention window, not a limiter input: the core counts live connections
// by refcount and needs no such guess. 30 minutes keeps an actively-streaming client (xray
// emits a fresh `accepted` line whenever it opens a TCP connection, so its timestamp
// refreshes well inside the window) listed continuously, while an address that has really
// stopped connecting drops off in bounded time instead of accumulating forever.
const ipStaleAfterSeconds = int64(30 * 60)

// NewCheckClientIpJob creates a new client IP monitoring job instance.
func NewCheckClientIpJob() *CheckClientIpJob {
	job = new(CheckClientIpJob)
	return job
}

func (j *CheckClientIpJob) Run() {
	if j.lastClear == 0 {
		j.lastClear = time.Now().Unix()
	}

	if !j.accessLogAvailable() {
		return
	}

	// Gated on some client somewhere having a cap because that is exactly when the panel
	// renders the IP log (the modal's row is gated on limitIp > 0). With no cap set
	// anywhere, parsing the log every 10s would produce rows nothing displays.
	if j.hasLimitIp() {
		j.processLogFile()
	}

	// Hourly only. The scrape no longer truncates the log to force a re-read (there is no
	// ban to re-trigger), so this is purely the access log's own rotation into the
	// persistent copy, which is what it was for whenever nothing was over its limit.
	if time.Now().Unix()-j.lastClear > 3600 {
		j.clearAccessLog()
	}
}

func (j *CheckClientIpJob) clearAccessLog() {
	logAccessP, err := os.OpenFile(xray.GetAccessPersistentLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	j.checkError(err)
	defer logAccessP.Close()

	accessLogPath, err := xray.GetAccessLogPath()
	j.checkError(err)

	file, err := os.Open(accessLogPath)
	j.checkError(err)
	defer file.Close()

	_, err = io.Copy(logAccessP, file)
	j.checkError(err)

	err = os.Truncate(accessLogPath, 0)
	j.checkError(err)

	// The file we have been tracking an offset into is now empty; the next scrape
	// must start from the top or it would skip everything Xray writes next.
	j.logOffset = 0
	j.lastClear = time.Now().Unix()
}

func (j *CheckClientIpJob) hasLimitIp() bool {
	now := time.Now().Unix()
	if j.limitIpCachedAt != 0 && now-j.limitIpCachedAt < limitIpCacheTTL {
		return j.limitIpCache
	}
	result := j.computeHasLimitIp()
	j.limitIpCache, j.limitIpCachedAt = result, now
	return result
}

func (j *CheckClientIpJob) computeHasLimitIp() bool {
	db := database.GetDB()

	// The inbound-level cap is a column, so ask the database instead of decoding
	// JSON to find out. Most panels that use IP limits at all set this one.
	var withColumnCap int64
	if err := db.Model(model.Inbound{}).Where("ip_limit > 0").Count(&withColumnCap).Error; err == nil && withColumnCap > 0 {
		return true
	}

	var inbounds []*model.Inbound

	// Only rows whose settings blob even mentions the key can carry a per-client
	// override, so the JSON decode below runs on a fraction of the table.
	err := db.Model(model.Inbound{}).Where("settings LIKE ?", "%limitIp%").Find(&inbounds).Error
	if err != nil {
		return false
	}

	for _, inbound := range inbounds {
		if inbound.Settings == "" {
			continue
		}

		settings := map[string][]model.Client{}
		json.Unmarshal([]byte(inbound.Settings), &settings)
		clients := settings["clients"]

		for _, client := range clients {
			limitIp := client.LimitIP
			if limitIp > 0 {
				return true
			}
		}
	}

	return false
}

// scanAccessLog reads the access log from `offset` and returns every (email -> ip ->
// last-seen) observation in the newly appended region, plus the offset to resume from.
//
// Split out of processLogFile so the offset arithmetic — the part that decides whether
// this job costs one line of work or the whole file — is exercised directly by tests
// rather than by a copy of it.
func scanAccessLog(f *os.File, offset int64) (map[string]map[string]int64, int64) {
	observed := make(map[string]map[string]int64, 100)

	// Resume where the last scrape stopped, and start over if the file shrank —
	// clearAccessLog truncates hourly, and an external rotation would do the same.
	// A file that shrank is a NEW file as far as our offset is concerned, so holding
	// the old offset would skip everything written after it.
	size := int64(0)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	if offset > size {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			offset = 0
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return observed, 0
			}
		}
	}

	// consumed counts only bytes belonging to COMPLETE lines. Xray appends to this
	// file while we read it, so the final chunk can be half a line; committing it
	// would make the next scrape resume mid-line and lose that entry. Leaving it
	// unconsumed means the next pass re-reads the partial line whole.
	//
	// ReadString rather than bufio.Scanner precisely because of that: Scanner hands
	// back the trailing partial line as an ordinary token at EOF with no way to tell
	// it apart, so counting its bytes silently swallowed the entry. ReadString
	// returns a nil error ONLY when it actually found the delimiter.
	consumed := offset
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := reader.ReadString('\n')
		if rerr != nil {
			break // EOF (possibly with a partial line, deliberately left unconsumed)
		}
		consumed += int64(len(line))
		line = strings.TrimRight(line, "\r\n")

		ipMatches := accessIPRegex.FindStringSubmatch(line)
		if len(ipMatches) < 2 {
			continue
		}
		ip := ipMatches[1]
		if ip == "127.0.0.1" || ip == "::1" {
			continue
		}

		emailMatches := accessEmailRegex.FindStringSubmatch(line)
		if len(emailMatches) < 2 {
			continue
		}
		email := emailMatches[1]

		var timestamp int64
		timestampMatches := accessTimestampRegex.FindStringSubmatch(line)
		if len(timestampMatches) >= 2 {
			t, err := time.Parse("2006/01/02 15:04:05", timestampMatches[1])
			if err == nil {
				timestamp = t.Unix()
			} else {
				timestamp = time.Now().Unix()
			}
		} else {
			timestamp = time.Now().Unix()
		}

		if _, exists := observed[email]; !exists {
			observed[email] = make(map[string]int64)
		}
		if existingTime, ok := observed[email][ip]; !ok || timestamp > existingTime {
			observed[email][ip] = timestamp
		}
	}
	return observed, consumed
}

func (j *CheckClientIpJob) processLogFile() {
	accessLogPath, _ := xray.GetAccessLogPath()
	file, err := os.Open(accessLogPath)
	if err != nil {
		return
	}
	defer file.Close()

	inboundClientIps, newOffset := scanAccessLog(file, j.logOffset)
	j.logOffset = newOffset

	for email, ipTimestamps := range inboundClientIps {

		// Convert to IPWithTimestamp slice
		ipsWithTime := make([]IPWithTimestamp, 0, len(ipTimestamps))
		for ip, timestamp := range ipTimestamps {
			ipsWithTime = append(ipsWithTime, IPWithTimestamp{IP: ip, Timestamp: timestamp})
		}

		clientIpsRecord, err := j.getInboundClientIps(email)
		if err != nil {
			j.addInboundClientIps(email, ipsWithTime)
			continue
		}

		j.updateInboundClientIps(clientIpsRecord, email, ipsWithTime)
	}
}

// mergeClientIps combines the persisted (old) and freshly observed (new)
// IP-with-timestamp lists for a single client into a map. An entry is
// dropped if its last-seen timestamp is older than staleCutoff.
//
// Extracted as a helper so updateInboundClientIps can stay DB-oriented
// and the merge policy can be exercised by a unit test.
func mergeClientIps(old, new []IPWithTimestamp, staleCutoff int64) map[string]int64 {
	ipMap := make(map[string]int64, len(old)+len(new))
	for _, ipTime := range old {
		if ipTime.Timestamp < staleCutoff {
			continue
		}
		ipMap[ipTime.IP] = ipTime.Timestamp
	}
	for _, ipTime := range new {
		if ipTime.Timestamp < staleCutoff {
			continue
		}
		if existingTime, ok := ipMap[ipTime.IP]; !ok || ipTime.Timestamp > existingTime {
			ipMap[ipTime.IP] = ipTime.Timestamp
		}
	}
	return ipMap
}

// accessLogAvailable reports whether Xray is writing an access log to read.
//
// Silent when it is not. The shipped template disables the access log ("access": "none"),
// and now that a cap is enforced without it, a client with a cap and no access log is a
// perfectly working limit with no IP list to show, not a misconfiguration: warning on
// every 10s tick would be pure noise about a feature that is doing its job.
func (j *CheckClientIpJob) accessLogAvailable() bool {
	accessLogPath, err := xray.GetAccessLogPath()
	if err != nil {
		return false
	}
	return accessLogPath != "none" && accessLogPath != ""
}

func (j *CheckClientIpJob) checkError(e error) {
	if e != nil {
		logger.Warning("client ip job err:", e)
	}
}

func (j *CheckClientIpJob) getInboundClientIps(clientEmail string) (*model.InboundClientIps, error) {
	db := database.GetDB()
	InboundClientIps := &model.InboundClientIps{}
	err := db.Model(model.InboundClientIps{}).Where("client_email = ?", clientEmail).First(InboundClientIps).Error
	if err != nil {
		return nil, err
	}
	return InboundClientIps, nil
}

func (j *CheckClientIpJob) addInboundClientIps(clientEmail string, ipsWithTime []IPWithTimestamp) error {
	inboundClientIps := &model.InboundClientIps{}
	jsonIps, err := json.Marshal(ipsWithTime)
	j.checkError(err)

	inboundClientIps.ClientEmail = clientEmail
	inboundClientIps.Ips = string(jsonIps)

	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Save(inboundClientIps).Error
	if err != nil {
		return err
	}
	return nil
}

// updateInboundClientIps folds this scan's addresses into the client's stored list.
//
// The client's own cap is deliberately not read here, and neither is its inbound: this
// records what was seen, and the core decides what to do about it. Every address is
// recorded, including one the core is refusing, because a view that hid it would answer
// "which addresses is this account being used from" with a lie, and that is the only
// question the list is asked.
func (j *CheckClientIpJob) updateInboundClientIps(inboundClientIps *model.InboundClientIps, clientEmail string, newIpsWithTime []IPWithTimestamp) {
	var oldIpsWithTime []IPWithTimestamp
	if inboundClientIps.Ips != "" {
		json.Unmarshal([]byte(inboundClientIps.Ips), &oldIpsWithTime)
	}

	// Merged rather than overwritten so an address stays listed while it is quiet and
	// across the hourly access log rotation, and expired at the cutoff so the list cannot
	// grow without bound. See ipStaleAfterSeconds.
	ipMap := mergeClientIps(oldIpsWithTime, newIpsWithTime, time.Now().Unix()-ipStaleAfterSeconds)

	dbIps := make([]IPWithTimestamp, 0, len(ipMap))
	for ip, ts := range ipMap {
		dbIps = append(dbIps, IPWithTimestamp{IP: ip, Timestamp: ts})
	}
	// Sorted oldest first, and by address for equal timestamps, because map order is
	// randomized per run: without this the blob's bytes churn on every tick and the panel
	// reorders the list under the operator's cursor for no reason.
	sort.Slice(dbIps, func(a, b int) bool {
		if dbIps[a].Timestamp != dbIps[b].Timestamp {
			return dbIps[a].Timestamp < dbIps[b].Timestamp
		}
		return dbIps[a].IP < dbIps[b].IP
	})

	jsonIps, _ := json.Marshal(dbIps)
	inboundClientIps.Ips = string(jsonIps)

	if err := database.GetDB().Save(inboundClientIps).Error; err != nil {
		logger.Error("failed to save inboundClientIps:", err)
	}
}
