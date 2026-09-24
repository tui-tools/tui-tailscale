#!/bin/bash
# Backend smoke test for tui-tailscale, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-tailscale on PATH).
#
# What a smoke test proves is that the tool reads the machine's *real* subject
# and agrees with the machine's own tooling. Nothing here changes the node: the
# assertions are reads (--report, --check), compared with what `tailscale`
# itself says. A guest without tailscale is a real case too — the tool has to
# say so, and say how to install it on that distribution.
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
# registration link, not an address of the host), so its line is left out.
check "check --demo carries no URL, name or address" \
  "$bin --demo --check | grep -v '\"loginUrl\":' | grep -cE '://|example|100\\.64\\.|fd7a:' || true" \
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

  check "check carries no URL of this node beyond a pending login's" \
    "$bin --check | grep -v '\"loginUrl\":' | grep -c '://' || true" \
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
        'sudo apt-get install -y tailscale'
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
        'sudo pacman -S --needed --noconfirm tailscale'
      ;;
  esac
fi

# --- compatibility evidence ------------------------------------------------
#
# record_compat turns this run into the evidence `tested` is generated from:
# one line per backend whose version the tool itself probed, printed behind
# `compat-result:` so it survives the trip out of the guest in the lab's log,
# and appended to $TUI_COMPAT_RESULTS as well for a run outside the lab.
# --check's compat block is a list, the same shape across the family.
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
# The probe is unprivileged, so --check as the lab user carries the same
# version the escalated run would; sudo -n is tried first only because it is
# what the rest of the family does.
report=$(sudo -n "$bin" --check 2>/dev/null || "$bin" --check 2>/dev/null)
record_compat "$report" "$outcome"

echo "--- tui-tailscale: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
