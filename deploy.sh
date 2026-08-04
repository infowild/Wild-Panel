#!/usr/bin/env bash
# Wild Panel installer / updater
set -euo pipefail

REPO="infowild/Wild-Panel"
ASSET="wild-panel-amd64"
LEGACY_ASSET="vpn-ui-amd64"          # pre-rebrand release asset (fallback download)
DEST_DIR="/opt/wild-panel"
LEGACY_DIR="/opt/vpn-ui"             # pre-rebrand install root
DEST="$DEST_DIR/$ASSET"
UNIT="wild-panel"
LEGACY_UNIT="vpn-ui"
# The management menu (`wild-panel`). Installed from INSIDE the binary we just placed
# ($DEST install-menu), never curled from the repo's default branch: that would pin
# a menu from a different release than the binary it drives.
MENU="/usr/bin/wild-panel"
LEGACY_MENU="/usr/bin/vpn-ui"
DL_URL="https://github.com/$REPO/releases/latest/download/$ASSET"
LEGACY_DL_URL="https://github.com/$REPO/releases/latest/download/$LEGACY_ASSET"
# The panel keeps its SQLite DB next to the binary (exe dir). Backups go beside it.
DB="$DEST_DIR/wild-panel.db"
BACKUP_DIR="$DEST_DIR/backups"
# Real-SSL (Let's Encrypt via acme.sh: Cloudflare DNS-01 or standalone HTTP-01).
# DEPLOY_DOMAIN / DEPLOY_EMAIL preset these for a non-interactive issuance;
# otherwise prompted. DEPLOY_CF_TOKEN (+ optional DEPLOY_WILDCARD=1) picks the
# Cloudflare path, and is read by the sourced obtain_letsencrypt_cert itself.
#
# A DEPLOY_DOMAIN holding a public IP rather than a name issues a certificate for
# that address, for a host with no domain at all. That path needs nothing new here:
# obtain_letsencrypt_cert recognises the literal and switches itself. Read its
# caveats before using it in an unattended install. The short version is that such a
# certificate lasts 160 hours rather than 90 days, so it renews every few days, and
# TCP :80 has to be reachable each time.
CERT_DIR="$DEST_DIR/cert"
DOMAIN="${DEPLOY_DOMAIN:-}"
EMAIL="${DEPLOY_EMAIL:-}"
PANEL_VERSION="2.0.2"
GITHUB_URL="https://github.com/$REPO"

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    B=$'\e[1m'; D=$'\e[2m'; R=$'\e[0m'
    BLUE=$'\e[38;5;39m'; GREEN=$'\e[38;5;114m'; RED=$'\e[38;5;203m'
    YELLOW=$'\e[38;5;221m'; TEAL=$'\e[38;5;44m'; WHITE=$'\e[1;38;5;255m'
    CYAN=$'\e[38;5;51m'; MAGENTA=$'\e[38;5;213m'
else
    B= D= R= BLUE= GREEN= RED= YELLOW= TEAL= WHITE= CYAN= MAGENTA=
fi

# ":: text"  bold-blue header + bold-white message (pacman's step style)
msg()  { printf '%s::%s %s%s%s\n' "$B$BLUE" "$R" "$WHITE" "$*" "$R"; }
# "  -> text"  blue action arrow
act()  { printf '  %s->%s %s\n' "$BLUE" "$R" "$*"; }
ok()   { printf '  %s->%s %s%s%s\n' "$GREEN" "$R" "$GREEN" "$*" "$R"; }
warn() { printf '%swarning:%s %s\n' "$B$YELLOW" "$R" "$*" >&2; }
die()  { printf '%serror:%s %s\n'   "$B$RED" "$R" "$*" >&2; exit 1; }
hr()   { printf '%s%s%s\n' "$D" "$(printf '%.0s─' {1..64})" "$R"; }

print_banner() {
    printf '\n'
    printf '%s%s' "$B$TEAL" "$R"
    cat <<'BANNER'
 ██╗    ██╗██╗██╗     ██████╗     ██████╗  █████╗ ███╗   ██╗███████╗██╗     
 ██║    ██║██║██║     ██╔══██╗    ██╔══██╗██╔══██╗████╗  ██║██╔════╝██║     
 ██║ █╗ ██║██║██║     ██║  ██║    ██████╔╝███████║██╔██╗ ██║█████╗  ██║     
 ██║███╗██║██║██║     ██║  ██║    ██╔═══╝ ██╔══██║██║╚██╗██║██╔══╝  ██║     
 ╚███╔███╔╝██║███████╗██████╔╝    ██║     ██║  ██║██║ ╚████║███████╗███████╗
  ╚══╝╚══╝ ╚═╝╚══════╝╚═════╝     ╚═╝     ╚═╝  ╚═╝╚═╝  ╚═══╝╚══════╝╚══════╝
BANNER
    printf '%s' "$R"
    printf '  %s%sAll-in-one VPN control panel%s\n' "$B" "$WHITE" "$R"
    printf '  %sVersion%s  %s%s%s\n' "$D" "$R" "$B$GREEN" "$PANEL_VERSION" "$R"
    printf '  %sGitHub%s   %s%s%s\n' "$D" "$R" "$CYAN" "$GITHUB_URL" "$R"
    printf '\n'
}

# Install packages the installer itself needs (download, TLS, archive, systemd helpers).
# Best-effort across apt / dnf / yum / apk / pacman. Skips packages already present.
install_prerequisites() {
    local need=() pkg missing=()
    for pkg in curl wget ca-certificates tar gzip unzip openssl; do
        case "$pkg" in
            ca-certificates)
                # Presence of the update-ca-certificates tool or the certs dir is enough.
                if command -v update-ca-certificates >/dev/null 2>&1 \
                   || [[ -d /etc/ssl/certs ]]; then
                    continue
                fi
                ;;
            *)
                command -v "$pkg" >/dev/null 2>&1 && continue
                ;;
        esac
        need+=("$pkg")
    done
    # At least one of curl/wget is enough for the download; keep both in `need`
    # only when neither exists.
    if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then
        local filtered=()
        for pkg in "${need[@]+"${need[@]}"}"; do
            [[ "$pkg" == "curl" || "$pkg" == "wget" ]] && continue
            filtered+=("$pkg")
        done
        need=("${filtered[@]+"${filtered[@]}"}")
    fi
    (( ${#need[@]} == 0 )) && { ok "prerequisites already present"; return 0; }

    msg "Installing prerequisites: ${need[*]}"
    if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -y >/dev/null 2>&1 || warn "apt-get update failed — trying install anyway"
        apt-get install -y "${need[@]}" || die "failed to install: ${need[*]}"
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y "${need[@]}" || die "failed to install: ${need[*]}"
    elif command -v yum >/dev/null 2>&1; then
        yum install -y "${need[@]}" || die "failed to install: ${need[*]}"
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache "${need[@]}" || die "failed to install: ${need[*]}"
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm "${need[@]}" || die "failed to install: ${need[*]}"
    else
        die "need packages (${need[*]}) but no supported package manager was found (apt/dnf/yum/apk/pacman)."
    fi
    ok "prerequisites ready"
}

# Byte counts the way a human reads them (one decimal from KB up). Shared by the
# live download line and its final summary, so the two can never disagree about
# units. Integer-only maths: no awk/bc dependency for something this small.
fmt_bytes() {
    local b="$1" div unit
    if   (( b >= 1073741824 )); then div=1073741824; unit=GB
    elif (( b >= 1048576    )); then div=1048576;    unit=MB
    elif (( b >= 1024       )); then div=1024;       unit=KB
    else printf '%d B' "$b"; return 0
    fi
    printf '%d.%d %s' "$(( b / div ))" "$(( b * 10 / div % 10 ))" "$unit"
}

# Compact durations for the ETA and the elapsed time: "45s", "2m07s".
fmt_time() {
    local s="$1"
    if (( s >= 60 )); then printf '%dm%02ds' "$(( s / 60 ))" "$(( s % 60 ))"
    else printf '%ds' "$s"
    fi
}

# Real-SSL (Let's Encrypt via acme.sh) lives in ONE place: obtain_letsencrypt_cert
# in wild-panel.sh, which is sourced further below once the menu script is installed.
# It used to be defined here and copied into the menu, which is exactly how two
# acme.sh flows drift apart. Sourcing (rather than running `wild-panel ssl`) keeps it
# in THIS shell, so its DOMAIN/EMAIL prompts fill in the variables the completion
# message below prints.

# Migrate a pre-rebrand install (/opt/vpn-ui) into /opt/wild-panel without dropping
# the operator's database, certs or backups. Safe to call repeatedly: no-op when the
# new tree already has a binary or when no legacy tree exists.
#
# IMPORTANT: keep the SQLite basename as vpn-ui.db here. A fallback download of the
# pre-rebrand binary still opens vpn-ui.db next to itself. A rebuilt Wild Panel
# binary renames vpn-ui.db → wild-panel.db on first start (config.LegacyDBPaths).
# Renaming in this script before that would hide the DB from a legacy binary.
migrate_legacy_install() {
    if [[ ! -d "$LEGACY_DIR" ]]; then
        return 0
    fi
    if [[ -e "$DEST" ]]; then
        return 0
    fi
    msg "Migrating legacy install ${LEGACY_DIR} → ${DEST_DIR}"
    if systemctl is-active --quiet "$LEGACY_UNIT" 2>/dev/null; then
        act "stopping ${LEGACY_UNIT}"
        systemctl stop "$LEGACY_UNIT" || true
    fi
    install -d -m 0755 "$DEST_DIR" "$BACKUP_DIR" "$CERT_DIR"
    local legacy_db="$LEGACY_DIR/vpn-ui.db"
    local dest_db="$DEST_DIR/vpn-ui.db"
    if [[ -f "$legacy_db" && ! -f "$dest_db" && ! -f "$DB" ]]; then
        if mv "$legacy_db" "$dest_db" 2>/dev/null; then
            ok "database → $dest_db"
        else
            cp -p "$legacy_db" "$dest_db"
            ok "database copied → $dest_db"
        fi
        for side in wal shm; do
            [[ -f "$legacy_db-$side" ]] || continue
            mv "$legacy_db-$side" "$dest_db-$side" 2>/dev/null \
                || cp -p "$legacy_db-$side" "$dest_db-$side" || true
        done
    fi
    if [[ -d "$LEGACY_DIR/cert" ]] && [[ -z "$(ls -A "$CERT_DIR" 2>/dev/null || true)" ]]; then
        cp -a "$LEGACY_DIR/cert/." "$CERT_DIR/" 2>/dev/null || true
        ok "certs → $CERT_DIR"
    fi
    if [[ -d "$LEGACY_DIR/backups" ]]; then
        cp -a "$LEGACY_DIR/backups/." "$BACKUP_DIR/" 2>/dev/null || true
    fi
    # Disable legacy unit so a reboot does not start two panels.
    if systemctl list-unit-files "$LEGACY_UNIT.service" 2>/dev/null | grep -q "$LEGACY_UNIT"; then
        systemctl disable "$LEGACY_UNIT" 2>/dev/null || true
        ok "disabled legacy unit ${LEGACY_UNIT}"
    fi
}

# Acquire root: re-exec through sudo when not already root, so `./deploy.sh`
# just works. If invoked piped (no script file) or without sudo, bail with
# instructions instead of failing obscurely.
if [[ $EUID -ne 0 ]]; then
    if [[ -f "$0" ]] && command -v sudo >/dev/null 2>&1; then
        exec sudo -- bash "$0" "$@"
    fi
    die "must run as root — use: sudo $0   (piped: curl -fsSL <url> | sudo bash)"
fi

# Preflight
print_banner
hr
printf '  %s[%sWild Panel%s]%s  deploy\n' "$B$TEAL" "$GREEN" "$TEAL" "$R"
hr

command -v systemctl >/dev/null 2>&1 || die "systemctl not found — this host isn't running systemd."

install_prerequisites

arch="$(uname -m)"
[[ "$arch" == "x86_64" || "$arch" == "amd64" ]] || \
    warn "host architecture is '$arch' — this installs the amd64 build, which may not run here."

migrate_legacy_install

# Fresh install vs in-place update: an already-installed binary (new or legacy path)
# means UPDATE. On update we must NOT re-randomize credentials (that would lock the
# operator out of their own panel) and we snapshot the DB before the new binary can migrate it.
MODE="install"; OLD_VER=""
if [[ -e "$DEST" ]]; then
    MODE="update"
    OLD_VER="$("$DEST" -v 2>/dev/null | tr -d '[:space:]')"
elif [[ -e "$LEGACY_DIR/$LEGACY_ASSET" ]]; then
    MODE="update"
    OLD_VER="$("$LEGACY_DIR/$LEGACY_ASSET" -v 2>/dev/null | tr -d '[:space:]')"
fi

if   command -v curl >/dev/null 2>&1; then DL="curl"
elif command -v wget >/dev/null 2>&1; then DL="wget"
else die "need 'curl' or 'wget' to download the release."; fi

# Resolve + download the latest release asset
msg "Fetching latest release of $REPO"

# Best-effort: read the release tag from the /releases/latest redirect (display only).
ver=""
if [[ "$DL" == "curl" ]]; then
    ver="$(curl -sILo /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null \
           | grep -oE 'tag/[^/[:space:]]+$' | sed 's#tag/##' || true)"
fi
[[ -n "$ver" ]] && act "latest release: ${GREEN}${ver}${R}" || act "asset: ${GREEN}${ASSET}${R}"
if [[ "$MODE" == "update" ]]; then
    act "mode:   ${YELLOW}update${R} (${OLD_VER:-unknown} -> ${ver:-latest})"
else
    act "mode:   ${GREEN}fresh install${R}"
fi

# Millisecond clock for the transfer rates below. GNU date has %3N; a date
# without it (busybox) falls back to whole seconds, which only coarsens the rate.
now_ms() {
    local t
    t="$(date +%s%3N 2>/dev/null || true)"
    [[ "$t" =~ ^[0-9]{13,}$ ]] || t="$(date +%s)000"
    printf '%s' "$t"
}

# Best-effort Content-Length, so the progress line can show a percentage and an
# ETA. GitHub redirects a release asset to its CDN, so follow the redirects and
# keep the LAST length seen. A miss here is not fatal: the line simply drops the
# percentage and the ETA and reports bytes plus rate.
# Discarding the length when the probe itself failed (pipefail carries the -f /
# --spider status out of the pipeline) is what stops a 404 page's own
# Content-Length from being presented as the asset's size.
remote_size() {
    local url="$1" len=""
    if [[ "$DL" == "curl" ]]; then
        len="$(curl -fsIL --max-time 20 "$url" 2>/dev/null \
               | grep -iE '^[[:space:]]*content-length:' | tail -n1 | tr -cd '0-9')" || len=""
    else
        len="$(wget --spider -S --tries=1 --timeout=20 "$url" 2>&1 \
               | grep -iE '^[[:space:]]*content-length:' | tail -n1 | tr -cd '0-9')" || len=""
    fi
    [[ "$len" =~ ^[0-9]+$ ]] && printf '%s' "$len"
    return 0
}

# One redraw of the live line. DL_LINE_LEN keeps the previous VISIBLE width so a
# line that shrinks (the ETA counting down) is padded over instead of leaving a
# tail behind; padding with blanks rather than an erase-to-end-of-line escape
# keeps this honest for anyone who sets NO_COLOR.
DL_BAR_FULL='####################'
DL_BAR_EMPTY='--------------------'
DL_LINE_LEN=0
DL_COLS=80
progress_line() {
    local bytes="$1" total="$2" speed="$3"
    local pct=0 pct_txt="" size_txt rate_txt eta_txt="" bar="" barlen=0 pad="" len
    size_txt="$(fmt_bytes "$bytes")"
    rate_txt="$(fmt_bytes "$speed")/s"
    if (( total > 0 )); then
        pct=$(( bytes * 100 / total ))
        (( pct > 100 )) && pct=100
        pct_txt="$(printf '%3d%%  ' "$pct")"
        size_txt="$size_txt / $(fmt_bytes "$total")"
        if (( speed > 0 && bytes < total )); then
            eta_txt="   eta $(fmt_time "$(( (total - bytes) / speed ))")"
        fi
    fi
    size_txt="$size_txt   "

    # The bar is the first thing to go on a narrow terminal: the numbers matter
    # more than the picture, and a wrapped line would break the \r redraw.
    len=$(( 5 + ${#pct_txt} + ${#size_txt} + ${#rate_txt} + ${#eta_txt} ))
    if (( total > 0 && DL_COLS - len >= 23 )); then
        bar="[${TEAL}${DL_BAR_FULL:0:$(( pct * 20 / 100 ))}${R}${D}${DL_BAR_EMPTY:0:$(( 20 - pct * 20 / 100 ))}${R}] "
        barlen=23
    fi
    len=$(( len + barlen ))
    (( DL_LINE_LEN > len )) && pad="$(printf '%*s' "$(( DL_LINE_LEN - len ))" '')"
    DL_LINE_LEN=$len
    printf '\r  %s->%s %s%s%s%s%s%s%s%s' \
        "$BLUE" "$R" "$bar" "$pct_txt" "$size_txt" "$GREEN" "$rate_txt" "$R" "$eta_txt" "$pad"
}

# Download $1 to $2 behind a progress line we render ourselves. curl and wget are
# both run quietly and the line is drawn from the partial file's size, the one
# signal the two tools share: that is what keeps the two paths looking identical,
# and it is the only way to show a rate on the wget path, whose --show-progress
# bar never printed one. Returns the transfer's REAL exit status (from wait, not
# from the backgrounding), so a failed download still reaches die.
fetch_asset() {
    local url="$1" out="$2"
    local total tick=0.2 rc=0 rated=0 live=0
    local start now bytes=0 last_ms last_b=0 speed=0 elapsed
    [[ -t 1 ]] && live=1     # a piped install (curl ... | sudo bash) gets no \r redraws

    # Terminal width once per download: recomputing it per redraw would fork five
    # times a second for a number that almost never changes.
    if (( live )); then DL_COLS="$(tput cols 2>/dev/null || echo 80)"; fi
    [[ "$DL_COLS" =~ ^[0-9]+$ ]] || DL_COLS=80
    # Fractional sleeps are a coreutils extension. Where they are missing, redraw
    # once a second instead of spinning on a failing sleep.
    sleep 0.01 2>/dev/null || tick=1

    total="$(remote_size "$url")"
    [[ "$total" =~ ^[0-9]+$ ]] || total=0
    # With no live line, the size is the only hint the log gives about how long
    # the silence is going to last.
    if (( ! live && total > 0 )); then act "size:   $(fmt_bytes "$total")"; fi

    # Quiet, but NOT mute: -sS / -nv drop the built-in progress bars (we draw the
    # line) while keeping each tool's own diagnosis, which is parked in DL_ERR and
    # replayed only if the transfer fails.
    DL_ERR="$(mktemp)"
    if [[ "$DL" == "curl" ]]; then
        curl -fL --retry 3 -sS -o "$out" "$url" 2>"$DL_ERR" &
    else
        wget --tries=3 -nv -O "$out" "$url" 2>"$DL_ERR" &
    fi
    DL_PID=$!

    start="$(now_ms)"; last_ms="$start"
    while kill -0 "$DL_PID" 2>/dev/null; do
        sleep "$tick"
        bytes="$(stat -c %s "$out" 2>/dev/null || echo 0)"
        now="$(now_ms)"
        if (( bytes < last_b )); then
            # A --retry restart truncates the file. Re-seed the window rather
            # than report a negative rate.
            last_b="$bytes"; last_ms="$now"; speed=0
        elif (( now - last_ms >= 800 )); then
            # Rate over the last ~second, not over the whole transfer: that is
            # what makes a stall visible instead of averaging it away.
            speed=$(( (bytes - last_b) * 1000 / (now - last_ms) ))
            last_b="$bytes"; last_ms="$now"; rated=1
        elif (( ! rated && now > start )); then
            speed=$(( bytes * 1000 / (now - start) ))
        fi
        if (( live )); then progress_line "$bytes" "$total" "$speed"; fi
    done
    wait "$DL_PID" || rc=$?
    DL_PID=""

    bytes="$(stat -c %s "$out" 2>/dev/null || echo 0)"
    elapsed=$(( $(now_ms) - start ))
    (( elapsed < 1 )) && elapsed=1
    DL_BYTES="$bytes"
    DL_SECS=$(( (elapsed + 500) / 1000 ))
    DL_RATE=$(( bytes * 1000 / elapsed ))

    if (( live )); then
        if (( rc == 0 )); then
            # Settle the line at 100% with the average rate, then free the row.
            progress_line "$bytes" "$total" "$DL_RATE"
            printf '\n'
        elif (( DL_LINE_LEN > 0 )); then
            printf '\r%*s\r' "$DL_LINE_LEN" ''   # wipe a half-drawn line before die
        fi
    fi
    # Quiet mode swallowed curl's/wget's own diagnosis ("HTTP 404" and friends),
    # which is exactly what an operator needs here, so replay it before we fail.
    if (( rc != 0 )) && [[ -s "$DL_ERR" ]]; then warn "$(tail -n1 "$DL_ERR")"; fi
    rm -f "$DL_ERR"; DL_ERR=""
    return "$rc"
}

install -d -m 0755 "$DEST_DIR"
tmp="$(mktemp "${DEST}.XXXXXX")"
DL_PID=""; DL_ERR=""
# One teardown for everything the download owns: the partial file, the captured
# stderr, and the transfer itself. Wired to INT/TERM as well as EXIT because a
# background job in a non-interactive shell ignores the Ctrl-C that reaches us,
# so without this it would keep downloading after the script is gone.
dl_cleanup() {
    if [[ -n "$DL_PID" ]] && kill -0 "$DL_PID" 2>/dev/null; then
        kill "$DL_PID" 2>/dev/null || true
        wait "$DL_PID" 2>/dev/null || true
    fi
    [[ -n "$DL_ERR" ]] && rm -f "$DL_ERR"
    rm -f "$tmp"
    return 0
}
trap 'dl_cleanup' EXIT
trap 'dl_cleanup; exit 130' INT
trap 'dl_cleanup; exit 143' TERM

msg "Downloading ${ASSET}"
if ! fetch_asset "$DL_URL" "$tmp"; then
    # Until a Wild Panel release publishes wild-panel-amd64, fall back to the
    # last vpn-ui-amd64 asset so install/upgrade still succeeds. The file is still
    # stored under the new DEST path so the rest of this script stays consistent.
    warn "primary asset '${ASSET}' unavailable — trying legacy '${LEGACY_ASSET}'"
    fetch_asset "$LEGACY_DL_URL" "$tmp" \
        || die "download failed from $DL_URL and $LEGACY_DL_URL — publish a release with '${ASSET}' (or legacy '${LEGACY_ASSET}')."
fi
# Back to the plain tmp-file cleanup for the rest of the run: nothing below this
# point owns a background job, so the download's signal handling ends here.
trap - INT TERM

# Sanity: non-empty and a real Linux ELF binary (not an HTML 404 page).
[[ -s "$tmp" ]] || die "downloaded file is empty."
if command -v file >/dev/null 2>&1; then
    file -b "$tmp" | grep -qi 'ELF' || die "downloaded file is not an ELF binary (got: $(file -b "$tmp"))."
else
    [[ "$(head -c4 "$tmp")" == $'\x7fELF' ]] || die "downloaded file is not an ELF binary."
fi
ok "downloaded $(fmt_bytes "$DL_BYTES") in $(fmt_time "$DL_SECS")  (avg $(fmt_bytes "$DL_RATE")/s)"

# Install the binary (stop the unit first if we're upgrading in place)
if systemctl is-active --quiet "$UNIT" 2>/dev/null; then
    act "stopping running ${UNIT} for replacement"
    systemctl stop "$UNIT" || true
fi
if systemctl is-active --quiet "$LEGACY_UNIT" 2>/dev/null; then
    act "stopping running ${LEGACY_UNIT} for replacement"
    systemctl stop "$LEGACY_UNIT" || true
fi
# Also reap a panel launched OUTSIDE systemd (bare ./wild-panel-amd64 or ./vpn-ui):
# the stop above only touches the unit, so a hand-launched panel would keep the
# web + Xray ports bound and collide with the unit we (re)start below.
if command -v pkill >/dev/null 2>&1; then
    pkill -x wild-panel-amd64 2>/dev/null || true
    pkill -x vpn-ui-amd64 2>/dev/null || true
    pkill -x vpn-ui 2>/dev/null || true
    pkill -x "$(basename "$DEST")" 2>/dev/null || true
fi

# Safety net: on update, snapshot the DB (timestamped + tagged with the outgoing
# version) before the new binary can touch or migrate it. The service is already
# stopped above, so copy the SQLite WAL/SHM sidecars alongside it for a consistent
# set. Abort if the copy fails — never replace the binary without a good backup.
# Prefer the new DB path; if migrate has not run yet, snapshot the legacy DB.
db_src=""
if [[ -f "$DB" ]]; then
    db_src="$DB"
elif [[ -f "$DEST_DIR/vpn-ui.db" ]]; then
    db_src="$DEST_DIR/vpn-ui.db"
elif [[ -f "$LEGACY_DIR/vpn-ui.db" ]]; then
    db_src="$LEGACY_DIR/vpn-ui.db"
fi
if [[ "$MODE" == "update" && -n "$db_src" ]]; then
    install -d -m 0755 "$BACKUP_DIR"
    ts="$(date +%Y%m%d-%H%M%S)"
    backup="$BACKUP_DIR/wild-panel_${OLD_VER:-unknown}_${ts}.db"
    cp -p "$db_src" "$backup" || die "DB backup failed ($db_src -> $backup) — aborting before replacing the binary."
    for side in wal shm; do
        [[ -f "$db_src-$side" ]] && cp -p "$db_src-$side" "$backup-$side" || true
    done
    ok "backed up DB -> $backup"
fi

chmod +x "$tmp"
mv -f "$tmp" "$DEST"
trap - EXIT
ok "installed -> $DEST"

# Install/refresh the management menu on BOTH paths (fresh install and update), so
# `wild-panel` always matches the binary that ships it. Must come before the TLS step
# below, which sources the menu for obtain_letsencrypt_cert.
msg "Installing the ${MENU} management menu"
# WILDPANEL_BIN / VPNUI_BIN: the menu (and the sourced SSL function) resolve the
# panel binary from these, so a non-default DEST_DIR carries through instead of
# falling back to the compiled-in default. Keep VPNUI_BIN for older embedded menus.
export WILDPANEL_BIN="$DEST"
export VPNUI_BIN="$DEST"
if "$DEST" install-menu >/dev/null 2>&1 && [[ -r "$MENU" ]]; then
    ok "management menu -> ${MENU}  (run: ${TEAL}wild-panel${R})"
    # Bring in obtain_letsencrypt_cert: the single implementation, shared rather
    # than copied. wild-panel.sh does nothing at top level when sourced (its menu is
    # behind a sourced/executed guard), so this only defines functions.
    # shellcheck source=wild-panel.sh
    source "$MENU"
    # Compat symlink so operators who still type `vpn-ui` keep working.
    if [[ ! -e "$LEGACY_MENU" || -L "$LEGACY_MENU" ]]; then
        ln -sfn "$MENU" "$LEGACY_MENU" 2>/dev/null || true
    fi
else
    warn "could not install ${MENU}, so the 'wild-panel' menu is unavailable on this host."
    # Keep the TLS branch below honest instead of letting an undefined function
    # abort the whole deploy: real SSL simply isn't on offer without the menu.
    obtain_letsencrypt_cert() { warn "real SSL needs ${MENU}, which failed to install. Skipping."; return 1; }
fi

# Configure + install/refresh the systemd unit. Fresh installs get randomized
# credentials (--random); updates DO NOT, so the operator's existing port, login
# and web path survive the upgrade.
if [[ "$MODE" == "install" ]]; then
    # Optional migration: import an existing 3x-ui (or vpn-ui) backup database before
    # the panel is configured, so an operator moving over keeps their inbounds,
    # clients, traffic and admin logins. The import preserves THIS install's own
    # port/path/TLS/secret, so only the operator's data comes across. Honour a preset
    # IMPORT_DB for non-interactive installs; otherwise ask on the controlling
    # terminal. A piped install with no IMPORT_DB set skips it and starts fresh.
    imported=""
    import_path="${IMPORT_DB:-}"
    if [[ -z "$import_path" && -r /dev/tty ]]; then
        {
            printf '%s::%s %sExisting 3x-ui data%s\n' "$B$BLUE" "$R" "$WHITE" "$R"
            printf '    Import inbounds, clients and traffic from a 3x-ui backup database?\n'
            printf '    Enter the path to the .db file, or leave blank to start fresh.\n'
            printf '  path: '
        } > /dev/tty
        read -r import_path < /dev/tty || import_path=""
    fi
    if [[ -n "$import_path" ]]; then
        if [[ -r "$import_path" ]]; then
            msg "Importing database from $import_path"
            if "$DEST" import --from "$import_path"; then
                imported="1"
                ok "Imported existing data. You will log in with that panel's credentials."
            else
                warn "Import failed, continuing with a fresh database."
            fi
        else
            warn "Cannot read '$import_path', continuing with a fresh database."
        fi
    fi

    # Panel transport: HTTP (default) or self-signed HTTPS. Honour PANEL_TLS when
    # preset (selfsign/https -> TLS; http -> plain); otherwise ask on the
    # controlling terminal. A piped, non-interactive install with no PANEL_TLS set
    # falls back to HTTP so `curl ... | sudo bash` never hangs on a prompt.
    tls_choice="http"
    case "${PANEL_TLS:-}" in
        letsencrypt|le|acme|real)        tls_choice="letsencrypt" ;;
        selfsign|https|self-signed|yes)  tls_choice="selfsign" ;;
        http|plain|0|no)                 tls_choice="http" ;;
        "")
            # A preset DEPLOY_DOMAIN or DEPLOY_CF_TOKEN implies a non-interactive
            # real-cert request; the sourced SSL function picks the challenge.
            if [[ -n "$DOMAIN" || -n "${DEPLOY_CF_TOKEN:-}" ]]; then
                tls_choice="letsencrypt"
            elif [[ -r /dev/tty ]]; then
                {
                    printf '%s::%s %sPanel access mode%s\n' "$B$BLUE" "$R" "$WHITE" "$R"
                    printf "    %s1)%s HTTPS  (real cert via Let's Encrypt: Cloudflare token, manual, or this server's IP)\n" "$GREEN" "$R"
                    printf '    %s2)%s HTTPS  (self-signed certificate)\n'                "$GREEN" "$R"
                    printf '    %s3)%s HTTP   (no TLS) %s[default]%s\n'                    "$GREEN" "$R" "$D" "$R"
                    printf '  choose [1/2/3]: '
                } > /dev/tty
                read -r _ans < /dev/tty || _ans=""
                case "$_ans" in
                    1) tls_choice="letsencrypt" ;;
                    2) tls_choice="selfsign" ;;
                esac
            fi
            ;;
        *) warn "unrecognized PANEL_TLS='${PANEL_TLS}' — defaulting to HTTP." ;;
    esac

    # Enable the chosen cert BEFORE --random so the randomized run sees the TLS
    # setting and prints an https:// URL. A failed Let's Encrypt attempt falls back
    # to plain HTTP rather than aborting the whole install.
    if [[ "$tls_choice" == "selfsign" ]]; then
        msg "Generating self-signed TLS certificate (HTTPS)"
        "$DEST" cert -selfsign
    elif [[ "$tls_choice" == "letsencrypt" ]]; then
        obtain_letsencrypt_cert || tls_choice="http"
    fi

    # Panel login / access: randomize everything (default) or enter custom values.
    # Ask on the controlling terminal; a piped, non-interactive install (curl ... |
    # sudo bash) has no tty and falls back to --random, so it never hangs on the
    # prompt nor installs empty credentials. The binary applies either choice with
    # the same work-safe stop/apply/restart envelope (--random / --user...--path).
    cred_mode="random"
    if [[ "$imported" == "1" ]]; then
        # The import brought its own admin login; randomizing now would throw it
        # away. Keep it and just install the unit (the port was preserved too).
        cred_mode="keep"
    elif [[ -r /dev/tty ]]; then
        {
            printf '%s::%s %sPanel login / access%s\n' "$B$BLUE" "$R" "$WHITE" "$R"
            printf '    %s1)%s Randomize  (port, username, password, web path) %s[default]%s\n' "$GREEN" "$R" "$D" "$R"
            printf '    %s2)%s Custom     (enter each value yourself)\n' "$GREEN" "$R"
            printf '  choose [1/2]: '
        } > /dev/tty
        read -r _cans < /dev/tty || _cans=""
        [[ "$_cans" == "2" ]] && cred_mode="custom"
    fi

    if [[ "$cred_mode" == "keep" ]]; then
        msg "Installing systemd unit (keeping the imported panel's login)"
        "$DEST" --systemd
    elif [[ "$cred_mode" == "custom" ]]; then
        msg "Enter panel login / access details (leave a field blank to keep the default)"
        printf '  %susername%s: ' "$BLUE" "$R" > /dev/tty; read -r  C_USER < /dev/tty || C_USER=""
        printf '  %spassword%s: ' "$BLUE" "$R" > /dev/tty; read -rs C_PASS < /dev/tty || C_PASS=""; printf '\n' > /dev/tty
        printf '  %sport%s: '     "$BLUE" "$R" > /dev/tty; read -r  C_PORT < /dev/tty || C_PORT=""
        printf '  %sweb path%s: ' "$BLUE" "$R" > /dev/tty; read -r  C_PATH < /dev/tty || C_PATH=""
        msg "Applying custom login / access + installing systemd unit"
        "$DEST" --user "$C_USER" --pass "$C_PASS" --port "$C_PORT" --path "$C_PATH" --systemd
    else
        msg "Configuring credentials + installing systemd unit"
        warn "--random sets a fresh port, username, password and web path — note them below."
        "$DEST" --random --systemd
    fi
else
    # Update: only touch TLS when explicitly requested (PANEL_TLS=letsencrypt, or a
    # DEPLOY_DOMAIN / DEPLOY_CF_TOKEN is set), so routine binary updates never change
    # the transport.
    if [[ "${PANEL_TLS:-}" =~ ^(letsencrypt|le|acme|real)$ || -n "$DOMAIN" || -n "${DEPLOY_CF_TOKEN:-}" ]]; then
        obtain_letsencrypt_cert || true
    fi
    msg "Refreshing systemd unit (existing credentials preserved)"
    "$DEST" --systemd
fi

msg "Starting ${UNIT}"
# A fallback download of an older binary may still register as vpn-ui. Prefer the
# new unit name, but start whichever unit file actually exists so install never
# leaves the panel down after a successful binary replace.
start_unit="$UNIT"
if ! systemctl cat "${UNIT}.service" >/dev/null 2>&1; then
    if systemctl cat "${LEGACY_UNIT}.service" >/dev/null 2>&1; then
        start_unit="$LEGACY_UNIT"
        warn "systemd unit is still '${LEGACY_UNIT}' (legacy binary) — will switch to '${UNIT}' once a Wild Panel build is published"
    fi
fi
systemctl restart "$start_unit"
sleep 1
if systemctl is-active --quiet "$start_unit"; then
    ok "${start_unit} is running"
else
    die "${start_unit} failed to start — inspect with: journalctl -u ${start_unit} -e"
fi

# Done
hr
msg "Deploy complete"
if [[ "$MODE" == "install" ]]; then
    if [[ "${cred_mode:-random}" == "keep" ]]; then
        act "sign in with the username / password from the panel you imported"
    elif [[ "${cred_mode:-random}" == "custom" ]]; then
        act "your custom login (port / user / password / web path) was applied — see above"
    else
        act "the randomized login (port / user / password / web path) is printed above"
    fi
    if [[ "${tls_choice:-http}" == "letsencrypt" ]]; then
        act "panel serves ${GREEN}HTTPS${R} with a real cert for ${TEAL}${DOMAIN}${WILDCARD_SAN:+ + ${WILDCARD_SAN}}${R}, no browser warning"
        act "auto-renew runs via acme.sh (cron); SSTP can reuse ${TEAL}$CERT_DIR/fullchain.pem${R} + ${TEAL}$CERT_DIR/privkey.pem${R}"
    elif [[ "${tls_choice:-http}" == "selfsign" ]]; then
        act "panel serves ${GREEN}HTTPS${R} with a self-signed cert — the browser warns once; accept it to continue"
    fi
else
    act "updated to ${GREEN}${ver:-latest}${R} — your existing port / login / web path are unchanged"
    # `[[ … ]] && act …` would return 1 when there was no backup, and under set -e
    # that ends the script right here, swallowing the status/logs lines below on
    # any update that found no DB to snapshot.
    if [[ -n "${backup:-}" ]]; then
        act "DB backup: ${TEAL}${backup}${R}"
    fi
fi
if [[ -x "$MENU" ]]; then
    act "manage:  ${TEAL}wild-panel${R}  (update, login, start/stop, Xray, SSL)"
    if [[ -L "$LEGACY_MENU" ]]; then
        act "compat:  ${TEAL}vpn-ui${R}  →  wild-panel"
    fi
fi
act "status:  ${TEAL}systemctl status ${start_unit}${R}"
act "logs:    ${TEAL}journalctl -u ${start_unit} -f${R}"
act "github:  ${TEAL}${GITHUB_URL}${R}"
hr
