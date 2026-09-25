package headscale

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The firewall hand-off (issue #32). The readiness line names the ports the
// host firewall keeps closed; f hands them to tui-firewall, which opens its
// add form prefilled with each one, still one preview and one confirm per
// rule: `tui-firewall --open 443/tcp,41641/udp --comment '…'`. That entry
// point exists since tui-firewall 0.6.0; an older one is launched plain, with
// a hint that says which ports to add by hand.

// ErrFirewallMissing is LaunchFirewall's answer when tui-firewall is not
// installed, which f turns into the offer to install it (issue #25).
var ErrFirewallMissing = errors.New(FirewallTool + " is not installed (it comes from pkgs.tui.tools)")

// FirewallOpenSince is the first tui-firewall release that takes --open.
const FirewallOpenSince = "0.6.0"

// MaxFirewallComment is tui-firewall's limit for --comment.
const MaxFirewallComment = 128

// FirewallPort is one port to open: a number and tcp or udp.
type FirewallPort struct {
	Port  int
	Proto string
	// Role says what the port is for, for the comment and the hint.
	Role string
}

// String is the port as tui-firewall's --open takes it: 443/tcp.
func (p FirewallPort) String() string { return strconv.Itoa(p.Port) + "/" + p.Proto }

// FirewallHandoff is what f passes to tui-firewall: the ports readiness found
// closed, and one comment for all of them. No port is a plain launch.
type FirewallHandoff struct {
	Open    []FirewallPort
	Comment string
}

// The roles of the ports readiness reads, in the order they are handed over.
const (
	RoleControl = "control plane"
	RoleNode    = "node"
	RoleSTUN    = "DERP STUN"
)

// ClosedPorts is the hand-off for a readiness: every port it read as closed,
// the control plane's first, and a comment naming what they are for
// ("tailnet control plane and node"). A port readiness could not judge is not
// handed over: tui-firewall shows the rules, and the user decides.
func ClosedPorts(r Readiness) FirewallHandoff {
	if r.Ports == nil {
		return FirewallHandoff{}
	}
	p := r.Ports
	var h FirewallHandoff
	add := func(port int, proto string, state PortState, role string) {
		if state != PortClosed || port < 1 || port > 65535 {
			return
		}
		for _, have := range h.Open {
			if have.Port == port && have.Proto == proto {
				return
			}
		}
		h.Open = append(h.Open, FirewallPort{Port: port, Proto: proto, Role: role})
	}
	add(p.ControlPort, "tcp", p.Control, RoleControl)
	add(p.NodePort, "udp", p.Node, RoleNode)
	add(p.STUNPort, "udp", p.STUN, RoleSTUN)
	if len(h.Open) == 0 {
		return FirewallHandoff{}
	}
	roles := make([]string, 0, len(h.Open))
	for _, port := range h.Open {
		roles = append(roles, port.Role)
	}
	h.Comment = "tailnet " + joinWords(roles)
	return h
}

// joinWords joins words as a sentence does: "a", "a and b", "a, b and c".
func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// commentPattern is a comment tui-firewall takes: one line of printable
// ASCII.
var commentPattern = regexp.MustCompile(`^[\x20-\x7e]+$`)

// Args is the argv tail tui-firewall is started with: none for a plain
// launch, --open and --comment otherwise. It holds the values to the rules
// tui-firewall 0.6.0 checks (a port 1-65535, tcp or udp, no port twice, a
// one-line comment of at most 128 characters), so the hand-off never ends in
// tui-firewall refusing to start.
func (h FirewallHandoff) Args() ([]string, error) {
	if len(h.Open) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	ports := make([]string, 0, len(h.Open))
	for _, p := range h.Open {
		if p.Port < 1 || p.Port > 65535 || (p.Proto != "tcp" && p.Proto != "udp") {
			return nil, fmt.Errorf("not a port to open: %d/%s", p.Port, p.Proto)
		}
		if seen[p.String()] {
			return nil, fmt.Errorf("port given twice: %s", p)
		}
		seen[p.String()] = true
		ports = append(ports, p.String())
	}
	args := []string{"--open", strings.Join(ports, ",")}
	if h.Comment != "" {
		if len(h.Comment) > MaxFirewallComment || !commentPattern.MatchString(h.Comment) ||
			strings.HasPrefix(h.Comment, "-") {
			return nil, fmt.Errorf("not a firewall comment: %q", h.Comment)
		}
		args = append(args, "--comment", h.Comment)
	}
	return args, nil
}

// Ports lists the ports as the hint says them: "443/tcp, 41641/udp".
func (h FirewallHandoff) Ports() string {
	ports := make([]string, 0, len(h.Open))
	for _, p := range h.Open {
		ports = append(ports, p.String())
	}
	return strings.Join(ports, ", ")
}

// versionPattern finds a version in `tui-firewall --version`'s answer
// ("tui-firewall 0.6.0", "tui-firewall v0.6.0-rc.1").
var versionPattern = regexp.MustCompile(`\bv?(\d+)\.(\d+)\.(\d+)`)

// ParseFirewallVersion reads the version out of `tui-firewall --version`,
// "" when there is none (a build from source says "dev").
func ParseFirewallVersion(out string) string {
	m := versionPattern.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2] + "." + m[3]
}

// FirewallTakesOpen reports whether a tui-firewall version has --open: 0.6.0
// or later. An unknown version does not: the launch falls back to plain.
func FirewallTakesOpen(version string) bool {
	return versionAtLeast(version, FirewallOpenSince)
}

// versionAtLeast compares two major.minor.patch versions.
func versionAtLeast(version, least string) bool {
	have, ok := versionParts(version)
	if !ok {
		return false
	}
	want, _ := versionParts(least)
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

// versionParts splits a major.minor.patch version into numbers.
func versionParts(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(fields) != 3 {
		return out, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// FirewallLaunch is a prepared hand-off: the process, and the hint for the
// status line when the ports could not be passed (an older tui-firewall).
type FirewallLaunch struct {
	Process Process
	// Prefilled reports that tui-firewall got the ports.
	Prefilled bool
	// Hint says why it did not, and what to add by hand.
	Hint string
}

// fallbackHint is the status line for a plain launch that should have been
// a prefilled one.
func fallbackHint(h FirewallHandoff, version string) string {
	have := "an unknown version"
	if version != "" {
		have = version
	}
	return "tui-firewall " + have + " cannot take the ports (--open needs " +
		FirewallOpenSince + "): a there adds " + h.Ports()
}
