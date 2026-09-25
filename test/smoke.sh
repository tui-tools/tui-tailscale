#!/bin/bash
# Backend smoke test for tui-tailscale, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-tailscale on PATH).
#
# What a smoke test proves is that the tool reads the machine's *real* subject
# and agrees with the machine's own tooling. The subject has two ends: this
# host as a node (`tailscale`) and the headscale control plane on it. Nothing
# here changes either: the assertions are reads (--report, --check), compared
# with what `tailscale`, `systemctl` and `stat` themselves say. A guest without
# tailscale or without headscale is a real case too — the tool has to say so,
# and say how to install it on that distribution.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-tailscale}"
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

echo "--- tui-tailscale smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"
echo "      user=$(id -un)"

# --- the report block ------------------------------------------------------
#
# --report is read-only and unprivileged, so it is smoked without sudo: a user
# who cannot escalate is exactly the one who most needs to be able to file a
# usable bug. The block goes into a public issue, so a home path or the host
# name appearing in it is a bug, not a cosmetic detail.
check "report names the backend" \
  "$bin --report" \
  '^backend: tailscale'

check "report says the run was live" \
  "$bin --report" \
  '^mode: live$'

check "report carries the client fact" \
  "$bin --report" \
  '^tailscale: '

check "report carries the daemon fact" \
  "$bin --report" \
  '^tailscaled: '

check "report works in demo mode too" \
  "$bin --demo --report" \
  '^backend: demo$'

check "and says so on the mode line" \
  "$bin --demo --report" \
  '^mode: demo'

# The distro and kernel lines are quoted from the machine's own description of
# itself, and a host named after its distribution ("fedora" on Fedora) would
# match there without anything having leaked. They are dropped before the
# search, so this stays a test of the tool rather than of the guest's hostname.
check "report leaks neither a home path nor the host name" \
  "$bin --report | grep -vE '^(distro|kernel): ' | grep -cE '/home/|$(uname -n)' || true" \
  '^0$'

# --- the check block, under --demo ------------------------------------------
#
# --check reads once and prints JSON. Under --demo it runs with nothing
# installed, so it is the read path that is always exercisable in the lab. It
# must carry no address, no name and no URL of the node or its tailnet.
check "check --demo is valid JSON naming the demo backend" \
  "$bin --demo --check" \
  '"backend": "demo"'

check "check --demo answers the login-server questions without the URL" \
  "$bin --demo --check" \
  '"https": true'

check "check --demo counts the peer offering an exit node" \
  "$bin --demo --check" \
  '"exitNodeOptions": 1'

# A pending login's URL is the one URL --check prints (loginUrl, a one-time
# registration link, not an address of the host), so its line is left out, and
# so are the two names the control-plane block prints on purpose: the OIDC
# issuer's host and dns.base_domain, the tailnet's own naming.
check "check --demo carries no URL, name or address" \
  "$bin --demo --check | grep -vE '\"(loginUrl|oidcIssuer|baseDomain)\":' | grep -cE '://|example|100\\.64\\.|fd7a:' || true" \
  '^0$'

# --- the check block, on this machine ---------------------------------------
if command -v tailscale >/dev/null 2>&1; then
  check "check says tailscale is installed" \
    "$bin --check" \
    '"installed": true'

  # The version the tool reads has to be the one the client prints.
  real_version=$(tailscale version 2>/dev/null | head -1 | grep -oE '^[0-9]+\.[0-9]+\.[0-9]+')
  check "check reads the client version tailscale prints" \
    "$bin --check" \
    "\"version\": \"${real_version:-none}\""

  # tailscaled's own answer, when it gives one, is the state --check reports.
  state=$(tailscale status --json 2>/dev/null | grep -m1 -oE '"BackendState": *"[A-Za-z]+"' |
    grep -oE '[A-Za-z]+"$' | tr -d '"')
  if [[ -n $state ]]; then
    check "check agrees with tailscale about the backend state" \
      "$bin --check" \
      "\"backendState\": \"$state\""
  else
    check "check says tailscaled did not answer" \
      "$bin --check" \
      '"daemonRunning": false'
  fi

  # The install blocks name the package repositories they fetch from (a
  # headscale install on a host without the tui-tools repository, say): those
  # are public URLs, not this node's, and are left out.
  check "check carries no URL of this node beyond a pending login's" \
    "$bin --check | grep -v '\"loginUrl\":' | grep -vE 'https://pkgs\.(tui\.tools|tailscale\.com)/' | grep -c '://' || true" \
    '^0$'
else
  # No client: the tool must say so, and give this distribution's commands.
  check "check says tailscale is not installed" \
    "$bin --check" \
    '"installed": false'

  manager=""
  command -v apt-get >/dev/null 2>&1 && manager=apt
  command -v dnf >/dev/null 2>&1 && manager=dnf
  command -v pacman >/dev/null 2>&1 && manager=pacman
  case "$manager" in
    apt)
      codename=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
      check "check gives the apt repository for this release" \
        "$bin --check" \
        "pkgs\\.tailscale\\.com/stable/[a-z]+/${codename}\\.tailscale-keyring\\.list"
      check "check gives the apt install" \
        "$bin --check" \
        'sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y tailscale'
      ;;
    dnf)
      check "check gives the dnf repository file" \
        "$bin --check" \
        '/etc/yum\.repos\.d/tailscale\.repo'
      check "check gives the dnf install" \
        "$bin --check" \
        'sudo dnf install -y tailscale'
      ;;
    pacman)
      check "check gives the pacman install" \
        "$bin --check" \
        'sudo pacman -Syu --needed --noconfirm tailscale'
      ;;
  esac
fi

# --- the control plane, under --demo ----------------------------------------
#
# The configuration read is what turned `oidc: yes/no` from a guess into a
# fact, so the block that carries it is smoked here. Under --demo it is the
# sample configuration; on a real host it is /etc/headscale/config.yaml.
check "check --demo carries the control-plane block" \
  "$bin --demo --check" \
  '"controlPlane"'

check "check --demo reports the control plane as OIDC-configured" \
  "$bin --demo --check" \
  '"oidcConfigured": true'

# The server_url is answered as two booleans rather than printed: those are the
# two ways an otherwise healthy setup fails, and neither names this host.
check "check --demo answers the server_url questions" \
  "$bin --demo --check" \
  '"serverUrlHttps": true'

check "check --demo says whether the server_url is loopback" \
  "$bin --demo --check" \
  '"serverUrlLoopback": false'

check "check --demo reduces the OIDC issuer to its host" \
  "$bin --demo --check" \
  '"oidcIssuer": "idp\.example\.com"'

# Whether the unit starts at boot is the half of "is it running" a fresh
# install gets wrong: the package leaves it disabled.
check "check --demo says whether the unit starts at boot" \
  "$bin --demo --check" \
  '"serviceEnabled": "disabled"'

# The ownership check: the demo's noise key is root's, the way a root-run
# `headscale configtest` leaves it, and --check names the path.
check "check --demo names a state file the service account does not own" \
  "$bin --demo --check" \
  '"path": "/var/lib/headscale/noise_private\.key"'

# The transport, read from the TLS settings and the bind: the demo sits behind
# a reverse proxy, with a MagicDNS domain outside its server_url host.
check "check --demo names the transport" \
  "$bin --demo --check" \
  '"transport": "reverse-proxy"'

check "check --demo reports the base domain without a conflict" \
  "$bin --demo --check" \
  '"baseDomainConflict": false'

check "check --demo keeps the inference as a separate field" \
  "$bin --demo --check" \
  '"oidcInferred":'

# The whole point of writing the secret to its own file: --check can say that
# one is set and has no field that could carry the value.
check "check --demo reports the secret as set, never its value" \
  "$bin --demo --check" \
  '"oidcClientSecretSet": true'

check "check --demo has no field that could hold a secret" \
  "$bin --demo --check | grep -icE '\"(oidc)?[a-z]*clientsecret\": \"' || true" \
  '^0$'

# A subnet router's routes stay pending until approved; --check counts them per
# node and never prints the networks. The demo's office-router has one.
check "check --demo counts the demo router's pending route" \
  "$bin --demo --check" \
  '"pending": 1'

check "check --demo prints no route" \
  "$bin --demo --check | grep -cE '0\.0\.0\.0/0|198\.51\.100\.|192\.0\.2\.' || true" \
  '^0$'

# The readiness line, as facts: the demo's unit runs but is disabled, so that
# is the next step.
check "check --demo names the next missing step" \
  "$bin --demo --check" \
  '"next": "unit"'

# 0.2.0: join profiles by name only, the dns section as counts, the waiting
# registration counted, and the firewall's ports as open/closed/unknown.
check "check --demo lists the join profiles by name" \
  "$bin --demo --check | tr -d ' \n'" \
  '"joinProfiles":\["laptop","subnet-router"\]'

check "check --demo counts the dns section" \
  "$bin --demo --check | tr -d ' \n'" \
  '"splitDomains":1,"searchDomains":1,"extraRecords":1'

check "check --demo counts the node waiting to register" \
  "$bin --demo --check" \
  '"pendingRegistrations": 2'

# 1.0.1: a registration the identity provider's policy refused is counted
# apart, and R does not offer it (issue #26).
check "check --demo counts the registration the IdP refused" \
  "$bin --demo --check" \
  '"refusedRegistrations": 1'

# 1.0.1: where the relays come from, and the note when they are Tailscale's
# public DERP servers (issue #27).
check "check --demo says the relays are Tailscale's public ones" \
  "$bin --demo --check" \
  '"relays": "tailscale-public"'

check "check --demo prints no registration id" \
  "$bin --demo --check | grep -c 'hskey-' || true" \
  '^0$'

check "check --demo reads the firewall's ports" \
  "$bin --demo --check | tr -d ' \n'" \
  '"ports":\{"source":"tui-firewall","controlPort":443,"control":"open","nodePort":41641,"node":"closed"\}'

# --- the control plane, on this machine ---------------------------------------
if command -v headscale >/dev/null 2>&1; then
  check "check says headscale is present" \
    "sudo -n $bin --check" \
    '"present": true'

  # The version the tool reads has to be the one headscale prints.
  hs_version=$(headscale version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
  check "check reads the version headscale prints" \
    "sudo -n $bin --check" \
    "\"version\": \"${hs_version:-none}\""

  # The unit's facts have to agree with systemd's own answers.
  enabled=$(systemctl is-enabled headscale 2>/dev/null | head -1)
  check "check agrees with systemctl about the unit starting at boot" \
    "sudo -n $bin --check" \
    "\"serviceEnabled\": \"${enabled:-unknown}\""
  active=$(systemctl is-active headscale 2>/dev/null | head -1)
  check "check agrees with systemctl about the unit running" \
    "sudo -n $bin --check" \
    "\"serviceState\": \"${active:-unknown}\""

  check "check read the configuration" \
    "sudo -n $bin --check" \
    '"readable": true'

  check "check reads a transport from the real configuration" \
    "sudo -n $bin --check" \
    '"transport": "(plain-http|letsencrypt|own-cert|reverse-proxy)"'

  # The check has to have run (stat reached the state directory through
  # sudo -n) and agree with stat about the directory.
  check "check ran the ownership check on the real state directory" \
    "sudo -n $bin --check" \
    '"ownershipChecked": true'
  owner=$(sudo -n stat -c %U:%G /var/lib/headscale 2>/dev/null)
  account=$(systemctl show headscale -p User --value 2>/dev/null)
  if [[ -n $owner && -n $account && ${owner%%:*} != "$account" ]]; then
    check "check reports the state directory owned by the wrong account" \
      "sudo -n $bin --check" \
      '"path": "/var/lib/headscale"'
  elif [[ -n $owner ]]; then
    check "check does not flag a state directory the service owns" \
      "sudo -n $bin --check | grep -c '\"path\": \"/var/lib/headscale\"' || true" \
      '^0$'
  fi

  # With the unit active the lists are read, and the node count is headscale's.
  if [[ $active == active ]]; then
    nodes=$(sudo -n headscale nodes list --output json 2>/dev/null | grep -cE '"(machine_key|machineKey)"')
    check "check counts the nodes headscale lists" \
      "sudo -n $bin --check" \
      "\"nodes\": ${nodes:-0},"
  fi

  check "check names the next missing step" \
    "sudo -n $bin --check" \
    '"next": "(server|unit|ports|identity|first-node|routes|ready)"'

  check "check carries no URL of this control plane" \
    "sudo -n $bin --check | grep -v '\"loginUrl\":' | grep -c '://' || true" \
    '^0$'

  # 1.1.0: a pre-auth key is shown once on screen and never reaches --check
  # (issue #30); headscale 0.26 and later print keys as hskey-auth-….
  check "check carries no pre-auth key" \
    "sudo -n $bin --check | grep -c 'hskey-auth-' || true" \
    '^0$'

  # 1.1.0: the relays follow derp.server.enabled, and with the embedded relay
  # on, readiness reads its STUN port (issue #28).
  derp_enabled=$(sudo -n cat /etc/headscale/config.yaml 2>/dev/null | awk '
    /^derp:/ { d = 1; next } /^[^ #]/ { d = 0 }
    d && /^  server:/ { s = 1; next } d && /^  [^ #]/ { s = 0 }
    s && /^    enabled:/ { print $2; exit }')
  if [[ $derp_enabled == true ]]; then
    check "check reports the embedded DERP relay config.yaml enables" \
      "sudo -n $bin --check" \
      '"relays": "embedded(\+tailscale-public)?"'
    stun=$(sudo -n cat /etc/headscale/config.yaml 2>/dev/null |
      grep -m1 -oE 'stun_listen_addr: *"?[^"]*:[0-9]+' | grep -oE '[0-9]+$')
    if [[ -n $stun ]] && sudo -n "$bin" --check 2>/dev/null | grep -q '"ports":'; then
      check "check reads the STUN port of the embedded relay" \
        "sudo -n $bin --check" \
        "\"stunPort\": ${stun},"
    fi
  elif [[ -n $derp_enabled ]]; then
    check "check reports no embedded relay while config.yaml has it off" \
      "sudo -n $bin --check" \
      '"relays": "(tailscale-public|custom)"'
  fi
else
  # No headscale: the tool must say so, give the next step, and this
  # distribution's commands from the tui-tools repository.
  check "check says headscale is not installed" \
    "$bin --check" \
    '"next": "install"'
  check "check gives the headscale install from the tui-tools repository" \
    "$bin --check" \
    '(apt-get install -y headscale|dnf install -y headscale|pacman -Syu --needed --noconfirm tui-tools/headscale|pacman -S --needed --noconfirm tui-tools/headscale)'
fi

# --- compatibility evidence ------------------------------------------------
#
# record_compat turns this run into the evidence `tested` is generated from:
# one line per backend whose version the tool itself probed, printed behind
# `compat-result:` so it survives the trip out of the guest in the lab's log,
# and appended to $TUI_COMPAT_RESULTS as well for a run outside the lab.
# --check's compat block is a list, the same shape across the family, with one
# entry per backend: the tailscale client and headscale.
TOOL=tui-tailscale
record_compat() {
  local report="$1" outcome="$2" distro today backend version line
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)
  local recorded=0
  while IFS=$'\t' read -r backend version; do
    [[ -n $backend && -n $version ]] || continue
    line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")
    printf 'compat-result: %s\n' "$line"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done < <(sed -n '/"compat": \[/,/^  \]/p' <<<"$report" | awk '
    /"backend":/ { if (b != "") print b "\t" v; gsub(/.*"backend": "|".*/, ""); b = $0; v = "" }
    /"version":/ { gsub(/.*"version": "|".*/, ""); v = $0 }
    END { if (b != "") print b "\t" v }')
  if [[ $recorded -eq 0 ]]; then
    echo "      no version was probed, so no compatibility result is recorded"
  fi
}

outcome=pass
[[ $fail -eq 0 ]] || outcome=fail
# The probes are unprivileged, so --check as the lab user carries the same
# versions the escalated run would; sudo -n is tried first because the
# control plane's read needs it.
report=$(sudo -n "$bin" --check 2>/dev/null || "$bin" --check 2>/dev/null)
record_compat "$report" "$outcome"

echo "--- tui-tailscale: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
